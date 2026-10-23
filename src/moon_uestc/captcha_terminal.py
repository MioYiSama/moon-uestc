"""Human-operated image and slider CAPTCHA prompts for interactive terminals."""

from __future__ import annotations

import sys
import warnings
from dataclasses import dataclass
from collections.abc import Callable
from io import BytesIO
from time import monotonic_ns
from typing import Literal, Protocol, cast

from PIL import Image
from prompt_toolkit.application import Application
from prompt_toolkit.formatted_text import ANSI
from prompt_toolkit.buffer import Buffer
from prompt_toolkit.input import Input
from prompt_toolkit.key_binding import KeyBindings
from prompt_toolkit.layout import Dimension, HSplit, Layout, Window
from prompt_toolkit.layout.controls import FormattedTextControl
from prompt_toolkit.output import Output
from prompt_toolkit.output.color_depth import ColorDepth
from prompt_toolkit.styles import Style
from prompt_toolkit.widgets import TextArea

from moon_uestc.errors import ProtocolError
from moon_uestc.idas import IDASClient
from moon_uestc.idas_models import SliderCaptcha, SliderTrack


_CANVAS_LENGTH = 280
_CANVAS_HEIGHT = 155
_BACKGROUND_DRAW_WIDTH = 278
_MAX_IMAGE_PIXELS = 16_000_000

_VERIFIED = object()


class _ImageResizer(Protocol):
    """Narrow Pillow's optional array-size overload to the tuple used here."""

    def resize(self, size: tuple[int, int], resample: int) -> Image.Image: ...


def _resize(image: Image.Image, size: tuple[int, int]) -> Image.Image:
    return cast(_ImageResizer, image).resize(size, Image.Resampling.LANCZOS)


@dataclass(slots=True)
class _SliderState:
    x: int = 0
    y: int = 0
    message: str = "Enter submit; r refresh; q quit"


def render_image(image: Image.Image, columns: int, max_rows: int) -> str:
    """Render a resized image with ANSI truecolor half-blocks."""
    if columns <= 0 or max_rows <= 0:
        return ""
    width, height = image.size
    if width <= 0 or height <= 0:
        raise ValueError("image dimensions must be positive")

    max_height = max_rows * 2
    scale = min(columns / width, max_height / height)
    target_width = max(1, min(columns, round(width * scale)))
    target_height = max(1, min(max_height, round(height * scale)))
    rgba = image.convert("RGBA")
    if rgba.size != (target_width, target_height):
        rgba = _resize(rgba, (target_width, target_height))
    rgb = Image.new("RGB", rgba.size, (0, 0, 0))
    rgb.paste(rgba, mask=rgba.getchannel("A"))
    pixels = rgb.tobytes()

    lines: list[str] = []
    for y in range(0, target_height, 2):
        pieces: list[str] = []
        for x in range(target_width):
            index = (y * target_width + x) * 3
            top = pixels[index:index + 3]
            if y + 1 < target_height:
                index += target_width * 3
                bottom = pixels[index:index + 3]
                pieces.append(
                    f"\x1b[38;2;{top[0]};{top[1]};{top[2]}"
                    f";48;2;{bottom[0]};{bottom[1]};{bottom[2]}m▀"
                )
            else:
                pieces.append(f"\x1b[38;2;{top[0]};{top[1]};{top[2]};48;2;0;0;0m▀")
        lines.append("".join(pieces) + "\x1b[0m")
    return "\n".join(lines)


def _decode_image(data: bytes) -> Image.Image:
    """Decode untrusted challenge bytes without exposing them in errors."""
    try:
        if not data:
            raise ValueError("empty image")
        with warnings.catch_warnings():
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            with Image.open(BytesIO(data)) as opened:
                width, height = opened.size
                if width <= 0 or height <= 0 or width * height > _MAX_IMAGE_PIXELS:
                    raise ValueError("unsupported image dimensions")
                opened.load()
                return opened.convert("RGBA")
    except Exception:
        raise ProtocolError("IDAS returned an invalid CAPTCHA image") from None


def _load_slider_images(captcha: SliderCaptcha) -> tuple[Image.Image, Image.Image]:
    background = _decode_image(captcha.background_image)
    if len(captcha.puzzle_image) <= 16:
        raise ProtocolError("IDAS returned an invalid CAPTCHA image")
    # The final 16 bytes of the site image are its signing key, not image data.
    piece = _decode_image(captcha.puzzle_image[:-16])
    try:
        scaled_background = _resize(background, (_BACKGROUND_DRAW_WIDTH, _CANVAS_HEIGHT))
        canvas = Image.new("RGBA", (_CANVAS_LENGTH, _CANVAS_HEIGHT), "white")
        canvas.alpha_composite(scaled_background, dest=(0, 0))
        piece_width = round(piece.width * _CANVAS_LENGTH / background.width - 2)
        if piece_width <= 0:
            raise ValueError("invalid piece dimensions")
        scaled_piece = _resize(piece, (piece_width, _CANVAS_HEIGHT))
        return canvas, scaled_piece
    except Exception:
        raise ProtocolError("IDAS returned an invalid CAPTCHA image") from None


def _composite_piece(
    background: Image.Image, piece: Image.Image, x: int, y: int
) -> Image.Image:
    frame = background.copy()
    left = max(0, x)
    top = max(0, y)
    right = min(background.width, x + piece.width)
    bottom = min(background.height, y + piece.height)
    if left >= right or top >= bottom:
        return frame
    visible_piece = piece.crop((left - x, top - y, right - x, bottom - y))
    frame.alpha_composite(visible_piece, dest=(left, top))
    return frame


def _application(
    body: HSplit,
    bindings: KeyBindings,
    *,
    input: Input | None,
    output: Output | None,
) -> Application[object]:
    return Application(
        layout=Layout(body),
        key_bindings=bindings,
        input=input,
        output=output,
        style=Style.from_dict({}),
        full_screen=True,
        erase_when_done=True,
        color_depth=ColorDepth.DEPTH_24_BIT,
    )


def _require_terminal(input: Input | None, output: Output | None) -> None:
    try:
        input_is_tty = input is not None or sys.stdin.isatty()
        output_is_tty = output is not None or sys.stdout.isatty()
    except (AttributeError, OSError):
        raise ProtocolError("CAPTCHA interaction requires a TTY") from None
    if not input_is_tty or not output_is_tty:
        raise ProtocolError("CAPTCHA interaction requires a TTY")


def _image_captcha(
    client: IDASClient,
    *,
    input: Input | None,
    output: Output | None,
) -> str:
    current_image = [_decode_image(client.get_image_captcha())]
    message = ["Enter submit | Ctrl-R/F5 refresh | Ctrl-C/Esc cancel"]
    def accept(buffer: Buffer) -> bool:
        _current_app().exit(result=buffer.text)
        return True

    answer = TextArea(
        prompt="Answer: ",
        multiline=False,
        wrap_lines=False,
        accept_handler=accept,
    )

    def image_text() -> ANSI:
        size = _current_app().output.get_size()
        return ANSI(render_image(current_image[0], size.columns, max(1, size.rows - 3)))

    def message_text() -> str:
        columns = _current_app().output.get_size().columns
        if columns < 40:
            return "Enter / ^R refresh / ^C cancel"
        if columns < 55:
            return "Enter submit / Ctrl-R refresh / Ctrl-C cancel"
        return message[0]

    body = HSplit(
        [
            Window(FormattedTextControl("IDAS image CAPTCHA"), height=1),
            Window(
                FormattedTextControl(image_text),
                height=Dimension(weight=1),
                wrap_lines=False,
            ),
            Window(FormattedTextControl(message_text), height=1),
            answer,
        ]
    )
    bindings = KeyBindings()

    def cancel(_event: object) -> None:
        app = _current_app()
        app.exit(exception=KeyboardInterrupt())

    def refresh(_event: object) -> None:
        app = _current_app()
        try:
            current_image[0] = _decode_image(client.get_image_captcha())
            answer.buffer.reset()
            app.invalidate()
        except Exception as exc:
            app.exit(exception=exc)

    bindings.add("c-c")(cancel)
    bindings.add("escape")(cancel)
    bindings.add("c-r")(refresh)
    bindings.add("f5")(refresh)
    result = _application(body, bindings, input=input, output=output).run()
    if not isinstance(result, str):
        raise KeyboardInterrupt()
    return result


def _current_app() -> Application[object]:
    from prompt_toolkit.application.current import get_app

    return cast(Application[object], get_app())


def _slider_captcha(
    client: IDASClient,
    *,
    input: Input | None,
    output: Output | None,
) -> None:
    captcha = client.get_slider_captcha()
    background, piece = _load_slider_images(captcha)
    state = _SliderState()
    tracks = [SliderTrack(a=0.0, b=0.0, c=0)]
    last_sample_ns = [monotonic_ns()]

    def frame_text() -> ANSI:
        size = _current_app().output.get_size()
        frame = _composite_piece(background, piece, state.x, 0)
        return ANSI(render_image(frame, size.columns, max(1, size.rows - 3)))

    def status_text() -> str:
        columns = _current_app().output.get_size().columns
        movement = (
            "arrows/hjkl 1px, Shift+arrows/HJKL 5px"
            if columns >= 70
            else "arrows 1px, HJKL 5px"
        )
        message = state.message
        if columns < 55 and message.startswith("Rejected"):
            message = "Rejected; move+Enter; r refresh; q quit"
        return f"x={state.x} y={state.y} | {movement}\n{message}"

    body = HSplit(
        [
            Window(FormattedTextControl("IDAS slider CAPTCHA"), height=1),
            Window(
                FormattedTextControl(frame_text),
                height=Dimension(weight=1),
                wrap_lines=False,
            ),
            Window(FormattedTextControl(status_text), height=2),
        ]
    )
    bindings = KeyBindings()

    def record_sample() -> None:
        now = monotonic_ns()
        elapsed_ms = max(0, (now - last_sample_ns[0]) // 1_000_000)
        tracks.append(
            SliderTrack(a=float(state.x), b=float(state.y), c=elapsed_ms)
        )
        last_sample_ns[0] = now

    def move(dx: int, dy: int) -> Callable[[object], None]:
        def handler(_event: object) -> None:
            app = _current_app()
            next_x = min(240, max(0, state.x + dx))
            next_y = min(_CANVAS_HEIGHT, max(-_CANVAS_HEIGHT, state.y + dy))
            if (next_x, next_y) != (state.x, state.y):
                state.x = next_x
                state.y = next_y
                record_sample()
                state.message = "Enter submit; r refresh; q quit"
                app.invalidate()

        return handler

    bindings.add("right")(move(1, 0))
    bindings.add("left")(move(-1, 0))
    bindings.add("up")(move(0, -1))
    bindings.add("down")(move(0, 1))
    bindings.add("h")(move(-1, 0))
    bindings.add("l")(move(1, 0))
    bindings.add("k")(move(0, -1))
    bindings.add("j")(move(0, 1))
    bindings.add("s-right")(move(5, 0))
    bindings.add("s-left")(move(-5, 0))
    bindings.add("s-up")(move(0, -5))
    bindings.add("s-down")(move(0, 5))
    bindings.add("H")(move(-5, 0))
    bindings.add("L")(move(5, 0))
    bindings.add("K")(move(0, -5))
    bindings.add("J")(move(0, 5))

    def cancel(_event: object) -> None:
        app = _current_app()
        app.exit(exception=KeyboardInterrupt())

    def refresh(_event: object) -> None:
        nonlocal background, piece
        app = _current_app()
        try:
            captcha = client.get_slider_captcha()
            background, piece = _load_slider_images(captcha)
            state.x = 0
            state.y = 0
            state.message = "Enter submit; r refresh; q quit"
            tracks[:] = [SliderTrack(a=0.0, b=0.0, c=0)]
            last_sample_ns[0] = monotonic_ns()
            app.invalidate()
        except Exception as exc:
            app.exit(exception=exc)

    def submit(_event: object) -> None:
        app = _current_app()
        if len(tracks) < 2 or state.x <= 0:
            state.message = "Move the piece before submitting; r refresh; q quit"
            app.invalidate()
            return
        record_sample()
        try:
            verified = client.verify_slider_captcha(
                canvas_length=float(_CANVAS_LENGTH),
                move_length=float(state.x),
                tracks=tuple(tracks),
            )
        except Exception as exc:
            app.exit(exception=exc)
            return
        if verified:
            app.exit(result=_VERIFIED)
        else:
            state.message = "Rejected; move and submit again; r refresh; q quit"
            app.invalidate()

    bindings.add("enter")(submit)
    bindings.add("r")(refresh)
    bindings.add("q")(cancel)
    bindings.add("escape")(cancel)
    bindings.add("c-c")(cancel)
    result = _application(body, bindings, input=input, output=output).run()
    if result is not _VERIFIED:
        raise KeyboardInterrupt()


def solve_captcha(
    client: IDASClient,
    kind: Literal["image", "slider"],
    *,
    input: Input | None = None,
    output: Output | None = None,
) -> str | None:
    """Display a CAPTCHA in the terminal and return its human-provided answer."""
    if kind not in ("image", "slider"):
        raise ValueError("unsupported CAPTCHA kind")
    _require_terminal(input, output)
    if kind == "image":
        return _image_captcha(client, input=input, output=output)
    _slider_captcha(client, input=input, output=output)
    return None
