import importlib.util
import io
import pathlib
import unittest
from contextlib import redirect_stderr
from unittest import mock


SCRIPT = pathlib.Path(__file__).with_name("seed-openftv-manager.py")
SPEC = importlib.util.spec_from_file_location("seed_openftv_manager", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
SPEC.loader.exec_module(MODULE)


class Response:
    status = 200

    def __init__(self, body: bytes):
        self.body = body

    def __enter__(self):
        return self

    def __exit__(self, *_args):
        return False

    def read(self):
        return self.body


class SeedOpenFtvManagerTest(unittest.TestCase):
    @mock.patch.object(MODULE.urllib.request, "urlopen")
    def test_manager_request_sends_bearer_token(self, urlopen):
        urlopen.return_value = Response(b"[]")

        status, body = MODULE.request("GET", "https://manager/v1/policies", "secret")

        request = urlopen.call_args.args[0]
        self.assertEqual((status, body), (200, b"[]"))
        self.assertEqual(request.get_header("Authorization"), "Bearer secret")

    @mock.patch.object(MODULE.urllib.request, "urlopen")
    def test_deployment_token_uses_password_grant(self, urlopen):
        urlopen.return_value = Response(b'{"access_token":"jwt"}')

        token = MODULE.deployment_token(
            "https://idp/realms/gbo", "openftv-manager-ui", "deployment", "pw"
        )

        request = urlopen.call_args.args[0]
        self.assertEqual(token, "jwt")
        self.assertEqual(
            request.full_url,
            "https://idp/realms/gbo/protocol/openid-connect/token",
        )
        self.assertEqual(
            request.data,
            b"grant_type=password&client_id=openftv-manager-ui&username=deployment&password=pw",
        )

    @mock.patch.object(MODULE, "request", return_value=(403, b"forbidden"))
    def test_prune_failure_fails_the_seed(self, _request):
        with redirect_stderr(io.StringIO()):
            result = MODULE.retire_stale("https://manager", set(), False, "jwt")
        self.assertEqual(result, 1)


if __name__ == "__main__":
    unittest.main()
