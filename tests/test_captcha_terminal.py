from __future__ import annotations

import io
import unittest
from collections.abc import Sequence
from typing import cast
from unittest.mock import patch

from PIL import Image
from prompt_toolkit.input import create_pipe_input
from prompt_toolkit.output import DummyOutput

from moon_uestc.captcha_terminal import solve_captcha
from moon_uestc.errors import ProtocolError
from moon_uestc.idas import IDASClient
from moon_uestc.idas_models import SliderCaptcha, SliderTrack


SIGNING_KEY = b"ABCDEFGHIJKLMNOP"


def png_bytes(size: tuple[int, int], color: tuple[int, int, int, int]) -> bytes:
    buffer = io.BytesIO()
    Image.new("RGBA", size, color).save(buffer, format="PNG")
    return buffer.getvalue()


class FakeCaptchaClient:
    def __init__(
        self,
        *,
        images: Sequence[bytes] = (),
        sliders: Sequence[SliderCaptcha] = (),
        verification_results: Sequence[bool] = (True,),
    ) -> None:
        self.images = tuple(images)
        self.sliders = tuple(sliders)
        self.verification_results = tuple(verification_results)
        self.image_calls = 0
        self.slider_calls = 0
        self.verifications: list[tuple[float, float, tuple[SliderTrack, ...]]] = []

    def get_image_captcha(self) -> bytes:
        result = self.images[min(self.image_calls, len(self.images) - 1)]
        self.image_calls += 1
        return result

    def get_slider_captcha(self) -> SliderCaptcha:
        result = self.sliders[min(self.slider_calls, len(self.sliders) - 1)]
        self.slider_calls += 1
        return result

    def verify_slider_captcha(
        self,
        *,
        canvas_length: float,
        move_length: float,
        tracks: Sequence[SliderTrack],
    ) -> bool:
        self.verifications.append((canvas_length, move_length, tuple(tracks)))
        index = min(len(self.verifications) - 1, len(self.verification_results) - 1)
        return self.verification_results[index]


def slider_captcha(
    *,
    background: bytes | None = None,
    piece: bytes | None = None,
) -> SliderCaptcha:
    return SliderCaptcha(
        background_image=background or png_bytes((278, 155), (170, 180, 190, 255)),
        puzzle_image=(piece or png_bytes((40, 155), (220, 40, 50, 180))) + SIGNING_KEY,
    )


class TerminalCaptchaTests(unittest.TestCase):
    def test_image_returns_manual_answer(self) -> None:
        client = FakeCaptchaClient(images=[png_bytes((12, 8), (200, 80, 20, 255))])
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("K7m2\r")
            result = solve_captcha(
                cast(IDASClient, client), "image", input=pipe_input, output=DummyOutput()
            )
        self.assertEqual(result, "K7m2")
        self.assertEqual(client.image_calls, 1)

    def test_image_refresh_fetches_new_challenge_and_clears_answer(self) -> None:
        client = FakeCaptchaClient(
            images=[
                png_bytes((8, 8), (20, 30, 40, 255)),
                png_bytes((8, 8), (50, 60, 70, 255)),
            ]
        )
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("K7\x12B4\r")
            result = solve_captcha(
                cast(IDASClient, client), "image", input=pipe_input, output=DummyOutput()
            )
        self.assertEqual(result, "B4")
        self.assertEqual(client.image_calls, 2)

    def test_slider_records_actual_scaled_keyboard_moves_and_timing(self) -> None:
        client = FakeCaptchaClient(sliders=[slider_captcha()])
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("lLJ\r")
            with patch(
                "moon_uestc.captcha_terminal.monotonic_ns",
                side_effect=[0, 10_000_000, 30_000_000, 32_000_000, 39_000_000],
            ):
                result = solve_captcha(
                    cast(IDASClient, client), "slider", input=pipe_input, output=DummyOutput()
                )
        self.assertIsNone(result)
        self.assertEqual(len(client.verifications), 1)
        canvas_length, move_length, tracks = client.verifications[0]
        self.assertEqual(canvas_length, 280.0)
        self.assertEqual(move_length, 6.0)
        self.assertEqual(
            [(track.a, track.b, track.c) for track in tracks],
            [(0.0, 0.0, 0), (1.0, 0.0, 10), (6.0, 0.0, 20), (6.0, 5.0, 2), (6.0, 5.0, 7)],
        )

    def test_slider_refresh_resets_position_and_samples(self) -> None:
        captcha = slider_captcha()
        client = FakeCaptchaClient(sliders=[captcha, captcha])
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("lrL\r")
            with patch(
                "moon_uestc.captcha_terminal.monotonic_ns",
                side_effect=[0, 10_000_000, 20_000_000, 25_000_000, 32_000_000],
            ):
                solve_captcha(
                    cast(IDASClient, client), "slider", input=pipe_input, output=DummyOutput()
                )
        self.assertEqual(client.slider_calls, 2)
        _, move_length, tracks = client.verifications[0]
        self.assertEqual(move_length, 5.0)
        self.assertEqual(
            [(track.a, track.b, track.c) for track in tracks],
            [(0.0, 0.0, 0), (5.0, 0.0, 5), (5.0, 0.0, 7)],
        )

    def test_enter_without_human_movement_does_not_submit(self) -> None:
        client = FakeCaptchaClient(sliders=[slider_captcha()])
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("\rq")
            with self.assertRaises(KeyboardInterrupt):
                solve_captcha(
                    cast(IDASClient, client), "slider", input=pipe_input, output=DummyOutput()
                )
        self.assertEqual(client.verifications, [])

    def test_slider_cancel_does_not_submit(self) -> None:
        client = FakeCaptchaClient(sliders=[slider_captcha()])
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("q")
            with self.assertRaises(KeyboardInterrupt):
                solve_captcha(
                    cast(IDASClient, client), "slider", input=pipe_input, output=DummyOutput()
                )
        self.assertEqual(client.verifications, [])

    def test_slider_rejection_is_not_treated_as_solved(self) -> None:
        client = FakeCaptchaClient(
            sliders=[slider_captcha()], verification_results=[False]
        )
        with create_pipe_input() as pipe_input:
            pipe_input.send_text("l\rq")
            with self.assertRaises(KeyboardInterrupt):
                solve_captcha(
                    cast(IDASClient, client), "slider", input=pipe_input, output=DummyOutput()
                )
        self.assertEqual(len(client.verifications), 1)
        self.assertEqual(client.verifications[0][1], 1.0)

    def test_default_non_tty_is_rejected_before_fetching(self) -> None:
        client = FakeCaptchaClient()
        with (
            patch("moon_uestc.captcha_terminal.sys.stdin", io.StringIO()),
            patch("moon_uestc.captcha_terminal.sys.stdout", io.StringIO()),
        ):
            with self.assertRaisesRegex(ProtocolError, "requires a TTY"):
                solve_captcha(cast(IDASClient, client), "image")
        self.assertEqual(client.image_calls, 0)

    def test_malformed_image_raises_sanitized_protocol_error(self) -> None:
        private_bytes = b"private-challenge-payload"
        client = FakeCaptchaClient(images=[private_bytes])
        with create_pipe_input() as pipe_input:
            with self.assertRaises(ProtocolError) as raised:
                solve_captcha(
                    cast(IDASClient, client),
                    "image",
                    input=pipe_input,
                    output=DummyOutput(),
                )
        self.assertNotIn(private_bytes.decode("ascii"), str(raised.exception))

    def test_malformed_slider_images_raise_protocol_error(self) -> None:
        client = FakeCaptchaClient(
            sliders=[slider_captcha(background=b"private-background-payload")]
        )
        with create_pipe_input() as pipe_input:
            with self.assertRaises(ProtocolError) as raised:
                solve_captcha(
                    cast(IDASClient, client),
                    "slider",
                    input=pipe_input,
                    output=DummyOutput(),
                )
        self.assertNotIn("private-background-payload", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
