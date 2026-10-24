from __future__ import annotations

import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from pydantic import SecretStr

import main
from moon_uestc import WeChatCredentials


class WeChatCacheTests(unittest.TestCase):
    def test_credentials_survive_private_cache_without_losing_cas(self) -> None:
        credentials = WeChatCredentials(
            openid=SecretStr("account-openid"),
            access_token=SecretStr("access-secret"),
            refresh_token=SecretStr("refresh-secret"),
            expires_at=1800000000,
        )
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "auth.json"
            with patch.object(main, "CACHE_PATH", path):
                main.save_cache(main.AuthCache(
                    session=SecretStr("moon-session"), castgc=SecretStr("cas-secret"),
                    wechat_credentials=credentials,
                ))
                cached = main.load_cache()
                self.assertEqual(cached.wechat_credentials, credentials)
                self.assertEqual(cached.session, SecretStr("moon-session"))
                self.assertEqual(cached.castgc, SecretStr("cas-secret"))
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertNotIn("access-secret", cached.model_dump_json())
                self.assertNotIn("refresh-secret", repr(cached))
                self.assertEqual(list(Path(directory).iterdir()), [path])

    def test_existing_cas_only_cache_remains_usable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "auth.json"
            path.write_text('{"session":"old-session","castgc":"old-cas"}')
            with patch.object(main, "CACHE_PATH", path):
                cached = main.load_cache()
            self.assertIsNone(cached.wechat_credentials)
            self.assertEqual(cached.session, SecretStr("old-session"))


if __name__ == "__main__":
    unittest.main()
