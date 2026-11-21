import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


INSTALLER = Path(__file__).with_name("install.sh").resolve()


class InstallTest(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory(prefix="pagetty test ")
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        self.home = root / "home"
        self.home.mkdir()
        self.source = root / "extracted"
        self.source.mkdir()
        shutil.copyfile(INSTALLER, self.source / "install.sh")
        self.dest = self.home / ".local/share/pagetty"
        self.work = self.home / ".pagetty-setup"
        self.rc = self.home / ".bashrc"
        self.profile = self.home / ".profile"
        self.rc.write_text('export PATH="/keep/first:$PATH:/keep/last"\n')
        self.profile.write_text('. "$HOME/.bashrc"\n')
        self.fixture_bin = root / "bin"
        self.fixture_bin.mkdir()
        self.env = {
            "HOME": str(self.home),
            "PATH": f"{self.fixture_bin}:/usr/bin:/bin",
        }
        self.version("first")
        wrappers = {
            "install": """if [[ $FAIL_STEP == copy && $* == *THIRD_PARTY_NOTICES.txt* ]]; then exit 23; fi""",
            "mv": """case "$FAIL_STEP:$*:@@{!#}" in
                record:*'/manifest '*:*) exit 23 ;;
                config:*'/apply/.profile '*:*) exit 23 ;;
                rollback:*'/apply/.profile '*:*|rollback:*'/before/.bashrc '*:*) exit 23 ;;
                place:*'/new '*:*) exit 23 ;;
              esac""".replace("@@{", chr(36) + "{"),
        }
        for tool, script in wrappers.items():
            path = self.fixture_bin / tool
            path.write_text(f'#!/bin/bash\n{script}\nexec /usr/bin/{tool} "$@"\n')
            path.chmod(0o755)

    def version(self, version):
        for name in ("pagetty", "LICENSE", "THIRD_PARTY_NOTICES.txt"):
            (self.source / name).write_text(f"{name} {version}\n")

    def run_setup(self, uninstall=False, step="", ok=True):
        script = self.dest / "uninstall.sh" if uninstall else self.source / "install.sh"
        result = subprocess.run(
            ["bash", str(script)], cwd=self.home, env=dict(self.env, FAIL_STEP=step),
            capture_output=True, text=True,
        )
        self.assertEqual(result.returncode == 0, ok, (result.stdout, result.stderr))
        return result

    def snapshot(self):
        return {
            str(path.relative_to(self.home)): (path.read_bytes(), path.stat().st_mode)
            for path in self.home.rglob("*") if path.is_file()
        }

    def test_install_reinstall_update_and_uninstall_without_source(self):
        start = self.snapshot()
        self.run_setup()
        first = self.snapshot()
        self.run_setup()
        self.assertEqual(self.snapshot(), first)
        path = subprocess.check_output(
            ["bash", "-lc", 'printf "%s" "$PATH"'], env=self.env, text=True,
        ).split(":")
        self.assertLess(path.index("/keep/first"), path.index("/keep/last"))
        self.assertEqual(path.count(str(self.dest)), 1)
        self.version("second")
        self.run_setup()
        self.assertEqual((self.dest / "pagetty").read_text(), "pagetty second\n")
        self.assertNotEqual(self.snapshot(), first)
        shutil.rmtree(self.source)
        self.run_setup(uninstall=True)
        self.assertFalse(self.dest.exists())
        self.assertEqual(self.snapshot(), start)

    def test_managed_file_replacements_or_links_are_preserved(self):
        self.run_setup()
        for name in ("pagetty", "LICENSE", "THIRD_PARTY_NOTICES.txt", "uninstall.sh"):
            path = self.dest / name
            content = path.read_bytes()
            # On uninstall the launcher must still be executable as a script.
            path.write_bytes(content + b"\n# replaced\n")
            expected = self.snapshot()
            self.run_setup(ok=False)
            self.run_setup(uninstall=True, ok=False)
            self.assertEqual(self.snapshot(), expected)
            path.write_bytes(content)
        external = self.home / "external"
        external.write_text("unrelated")
        path = self.dest / "LICENSE"
        path.unlink()
        path.symlink_to(external)
        self.run_setup(ok=False)
        self.run_setup(uninstall=True, ok=False)
        self.assertTrue(path.is_symlink())
        self.assertEqual(external.read_text(), "unrelated")

    def test_refuses_unmanaged_folder(self):
        self.dest.mkdir(parents=True)
        (self.dest / "pagetty").write_text("unrelated")
        before = self.snapshot()
        self.run_setup(ok=False)
        self.assertFalse(self.work.exists())
        self.assertEqual(self.snapshot(), before)

    def test_refuses_hardlinked_or_symlinked_startup_file(self):
        for path in (self.rc, self.profile):
            alias = self.home / "alias"
            alias.hardlink_to(path)
            before = self.snapshot()
            self.run_setup(ok=False)
            self.assertEqual(self.snapshot(), before)
            self.assertTrue(path.samefile(alias))
            alias.unlink()
        self.profile.rename(self.home / "external")
        self.profile.symlink_to(self.home / "external")
        before = self.snapshot()
        self.run_setup(ok=False)
        self.assertEqual(self.snapshot(), before)
        self.assertFalse(self.dest.exists())

    def test_failures_preserve_fresh_or_previous_state(self):
        for update in (False, True):
            if update:
                self.run_setup()
                self.version("second")
                # Force the next update to apply startup changes too.
                self.rc.write_text('export PATH="/keep/first:$PATH:/keep/last"\n')
                self.profile.write_text('. "$HOME/.bashrc"\n')
            before = self.snapshot()
            for step in ("copy", "record", "place", "config"):
                with self.subTest(update=update, step=step):
                    self.run_setup(step=step, ok=False)
                    self.assertEqual(self.dest.exists(), update)
                    self.assertFalse(self.work.exists())
                    self.assertEqual(self.snapshot(), before)

    def test_uninstall_failure_restores_old_payload_and_config(self):
        self.run_setup()
        before = self.snapshot()
        self.run_setup(uninstall=True, step="config", ok=False)
        self.assertEqual(self.snapshot(), before)

    def test_reports_incomplete_rollback(self):
        self.run_setup()
        self.rc.write_text("# user bashrc\n")
        self.profile.write_text("# user profile\n")
        result = self.run_setup(step="rollback", ok=False)
        self.assertIn("Rollback incomplete", result.stderr)
        self.assertTrue((self.work / "before/.bashrc").is_file())
        self.assertEqual((self.dest / "pagetty").read_text(), "pagetty first\n")

    def test_keeps_duplicate_hook_and_user_changes(self):
        self.run_setup()
        original_rc = self.rc.read_text()
        original_profile = self.profile.read_text()
        self.rc.write_text(original_rc + "# added by user\n")
        self.profile.write_text(original_profile + original_profile.splitlines()[-1] + "\n")
        result = self.run_setup(uninstall=True)
        self.assertIn("Check the PATH line manually", result.stderr)
        self.assertTrue(self.rc.read_text().endswith("# added by user\n"))
        self.assertNotIn("PageTTY installer", self.rc.read_text())
        self.assertEqual(self.profile.read_text().count("PageTTY installer"), 2)


if __name__ == "__main__":
    unittest.main()
