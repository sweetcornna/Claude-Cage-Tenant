import importlib.util
import os
from pathlib import Path
import stat
import sys
import tempfile
import time
import unittest

spec = importlib.util.spec_from_file_location('resource_governor', Path(__file__).with_name('resource-governor.py'))
governor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(governor)

MIB = 1 << 20
GIB = 1 << 30


class Guest:
    """A fake guest: cgroup root, /run directory, proc files and systemctl."""

    def __init__(self, root, total=3 * GIB, recursiveprot=True):
        self.root = Path(root)
        self.calls = self.root / 'calls'
        for name in ('cgroup', 'run/in', 'proc'):
            (self.root / name).mkdir(parents=True, exist_ok=True)
        (self.root / 'cgroup' / 'cgroup.controllers').write_text('cpu memory\n')
        for unit, current, pressure in (('system.slice', 400 * MIB, '0.50'), ('user.slice', 513 * MIB, '0.00'), ('cage-browser.slice', 1700 * MIB, '31.00')):
            directory = self.root / 'cgroup' / unit
            directory.mkdir()
            (directory / 'memory.current').write_text('%d\n' % current)
            (directory / 'memory.low').write_text('0\n')
            (directory / 'memory.high').write_text('max\n')
            (directory / 'memory.max').write_text('max\n')
            (directory / 'memory.pressure').write_text('some avg10=%s avg60=0.00 avg300=0.00 total=1\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=1\n' % pressure)
        (self.root / 'proc' / 'meminfo').write_text('MemTotal:       %d kB\nMemFree: 1 kB\n' % (total // 1024))
        (self.root / 'proc' / 'uptime').write_text('123.45 100.00\n')
        options = 'rw,nosuid,nodev,noexec,relatime,nsdelegate' + (',memory_recursiveprot' if recursiveprot else '')
        (self.root / 'proc' / 'mounts').write_text('proc /proc proc rw 0 0\ncgroup2 %s cgroup2 %s 0 0\n' % (self.root / 'cgroup', options))
        recorder = self.root / 'systemctl'
        recorder.write_text('#!/bin/sh\nprintf "%%s\\n" "$*" >> %s\n' % self.calls)
        recorder.chmod(0o755)
        governor.CGROUP = str(self.root / 'cgroup')
        governor.RUN = str(self.root / 'run')
        governor.MEMINFO = str(self.root / 'proc' / 'meminfo')
        governor.UPTIME = str(self.root / 'proc' / 'uptime')
        governor.MOUNTS = str(self.root / 'proc' / 'mounts')
        governor.SYSTEMCTL = [str(recorder)]

    def limits(self, text, age=0):
        path = self.root / 'run' / 'in' / 'limits'
        path.write_text(text)
        stamp = time.time() - age
        os.utime(path, (stamp, stamp))

    def called(self):
        if not self.calls.exists():
            return []
        lines = self.calls.read_text().splitlines()
        self.calls.unlink()
        return lines


class GovernorTest(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.guest = Guest(self.directory.name)

    def tearDown(self):
        self.directory.cleanup()

    def test_plan_matches_the_host_agent(self):
        # TestGuestClassLimits in guest_allocation_test.go: the evidence guest.
        self.assertEqual(governor.plan(3 * GIB, 400 * MIB, 513 * MIB),
                         {'control_low': 576 * MIB, 'sessions_low': 832 * MIB, 'browser_high': 1600 * MIB, 'browser_max': 1856 * MIB})
        self.assertGreaterEqual(governor.plan(6 * GIB, 400 * MIB, 513 * MIB)['browser_high'], 4 * GIB)

    def test_host_limits_are_clamped(self):
        self.guest.limits('version 1\ncontrol_low %d\nsessions_low %d\nsessions_high 1\nbrowser_high 1\nbrowser_max 2\n' % (8 * GIB, 8 * GIB))
        limits = governor.received(time.time(), 3 * GIB)
        self.assertEqual(limits, {'control_low': 768 * MIB, 'sessions_low': 1536 * MIB, 'browser_high': 768 * MIB, 'browser_max': 1024 * MIB})
        # Stale, unversioned, incomplete or not a regular file: not used.
        self.guest.limits('version 1\ncontrol_low 1\nsessions_low 1\nbrowser_high 1\nbrowser_max 1\n', age=30)
        self.assertIsNone(governor.received(time.time(), 3 * GIB))
        self.guest.limits('control_low 1\nsessions_low 1\nbrowser_high 1\nbrowser_max 1\n')
        self.assertIsNone(governor.received(time.time(), 3 * GIB))
        self.guest.limits('version 1\ncontrol_low 1\n')
        self.assertIsNone(governor.received(time.time(), 3 * GIB))
        path = Path(governor.RUN) / 'in' / 'limits'
        path.unlink()
        target = Path(self.directory.name) / 'elsewhere'
        target.write_text('version 1\ncontrol_low 1\nsessions_low 1\nbrowser_high 1\nbrowser_max 1\n')
        path.symlink_to(target)
        self.assertIsNone(governor.received(time.time(), 3 * GIB))

    def test_applies_host_limits_with_hysteresis_and_reports(self):
        self.guest.limits('version 1\ncontrol_low %d\nsessions_low %d\nbrowser_high %d\nbrowser_max %d\n' % (576 * MIB, 832 * MIB, 4 * GIB, 4 * GIB + 384 * MIB))
        g = governor.Governor()
        g.once()
        self.assertEqual(sorted(self.guest.called()), sorted([
            'set-property --runtime cage-browser.slice MemoryHigh=%d' % (4 * GIB),
            'set-property --runtime cage-browser.slice MemoryMax=%d' % (4 * GIB + 384 * MIB),
            'set-property --runtime system.slice MemoryLow=%d' % (576 * MIB),
            'set-property --runtime user.slice MemoryLow=%d' % (832 * MIB)]))
        state = (Path(governor.RUN) / 'state').read_text().splitlines()
        self.assertEqual(state[0], 'governor 1 123.5')
        self.assertIn('class control %d 0.50 0 0 0' % (400 * MIB), state)
        self.assertIn('class browser %d 31.00 0 0 0' % (1700 * MIB), state)
        self.assertEqual(stat.S_IMODE(os.stat(Path(governor.RUN) / 'state').st_mode), 0o644)
        # Less than 64 MiB of change: nothing is reapplied.
        self.guest.limits('version 1\ncontrol_low %d\nsessions_low %d\nbrowser_high %d\nbrowser_max %d\n' % (600 * MIB, 832 * MIB, 4 * GIB + 32 * MIB, 4 * GIB + 384 * MIB))
        g.once()
        self.assertEqual(self.guest.called(), [])

    def test_without_host_limits_the_guest_size_decides(self):
        g = governor.Governor()
        g.once()
        calls = self.guest.called()
        self.assertIn('set-property --runtime cage-browser.slice MemoryHigh=%d' % (1600 * MIB), calls)
        self.assertIn('set-property --runtime cage-browser.slice MemoryMax=%d' % (1856 * MIB), calls)

    def test_no_protection_without_recursive_protection(self):
        self.directory.cleanup()
        self.directory = tempfile.TemporaryDirectory()
        self.guest = Guest(self.directory.name, recursiveprot=False)
        governor.Governor().once()
        calls = self.guest.called()
        self.assertEqual(len(calls), 2)
        self.assertFalse(any('MemoryLow' in call for call in calls))

    def test_reset_and_unsupported_hierarchy(self):
        governor.Governor().once()
        self.guest.called()
        governor.reset()
        self.assertEqual(sorted(self.guest.called()), sorted([
            'set-property --runtime cage-browser.slice MemoryHigh=infinity',
            'set-property --runtime cage-browser.slice MemoryMax=infinity',
            'set-property --runtime system.slice MemoryLow=0',
            'set-property --runtime user.slice MemoryLow=0']))
        self.assertFalse((Path(governor.RUN) / 'state').exists())
        (Path(governor.CGROUP) / 'cgroup.controllers').unlink()
        self.assertEqual(governor.main(['governor']), 0)
        self.assertEqual(self.guest.called(), [])

    def test_failed_steps_do_not_stop_the_governor(self):
        governor.SYSTEMCTL = ['/nonexistent/systemctl']
        g = governor.Governor()
        g.once()
        self.assertTrue((Path(governor.RUN) / 'state').exists())


if __name__ == '__main__':
    unittest.main()
