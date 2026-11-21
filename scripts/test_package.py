import contextlib
import importlib.util
import io
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


spec = importlib.util.spec_from_file_location("package_script", Path(__file__).with_name("package.py"))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class PackageWorkspaceTest(unittest.TestCase):
    def test_go_commands_ignore_parent_workspace(self):
        root = Path(package.__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as temp:
            out = Path(temp) / "dist"
            parent = dict(os.environ, GOWORK=str(Path(temp) / "go.work"), PAGETTY_TEST_ENV="kept")
            def build(args, **kwargs):
                Path(args[args.index("-o") + 1]).write_bytes(b"binary")
            with mock.patch.dict(os.environ, parent, clear=True), \
                 mock.patch.object(package.subprocess, "check_output", return_value="go1.27.4") as version, \
                 mock.patch.object(package.subprocess, "run", side_effect=build) as builds, \
                 mock.patch("sys.argv", ["package.py", "--output-dir", str(out)]), \
                 contextlib.redirect_stdout(io.StringIO()):
                package.main()
                self.assertEqual(dict(os.environ), parent)
                calls = [version.call_args, *builds.call_args_list]
                self.assertEqual(len(calls), 3)
                for call in calls:
                    self.assertEqual(call.kwargs["cwd"], root)
                    self.assertEqual(call.kwargs["env"]["GOWORK"], "off")
                    self.assertEqual(call.kwargs["env"]["PAGETTY_TEST_ENV"], "kept")
                self.assertEqual([c.kwargs["env"]["GOOS"] for c in builds.call_args_list], ["windows", "linux"])


if __name__ == "__main__":
    unittest.main()
