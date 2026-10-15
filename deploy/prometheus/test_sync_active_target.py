import importlib.util
import io
from pathlib import Path
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("sync_active_target", Path(__file__).with_name("sync-active-target.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ActiveTargetTests(unittest.TestCase):
    def test_both_slots_keep_one_stable_instance(self):
        for port in ("8781", "8782"):
            data = module.target_data(port + "\n", "gateway-245")
            self.assertEqual(data[0]["targets"], ["127.0.0.1:" + port])
            self.assertEqual(data[0]["labels"]["instance"], "gateway-245")

    def test_invalid_active_port_never_replaces_discovery(self):
        for port in ("", "8783", "8781\n8782", "localhost:8781"):
            source, output = Mock(), Mock()
            source.read_text.return_value = port
            with patch.object(module.os, "replace") as replace:
                with self.assertRaises(ValueError):
                    module.sync(source, output, "gateway-245")
                replace.assert_not_called()
                output.read_text.assert_not_called()

    def test_unchanged_target_avoids_rewrites(self):
        source, output = Mock(), Mock()
        source.read_text.return_value = "8781"
        output.read_text.return_value = module.json.dumps(module.target_data("8781", "gateway-245"), sort_keys=True, indent=2) + "\n"
        with patch.object(module.tempfile, "NamedTemporaryFile") as temporary:
            self.assertFalse(module.sync(source, output, "gateway-245"))
            temporary.assert_not_called()

    def test_switch_atomically_publishes_new_target(self):
        source, output = Mock(), Mock()
        source.read_text.return_value = "8782"
        output.read_text.return_value = "old target"
        content = io.StringIO()
        fh = Mock()
        fh.__enter__ = Mock(return_value=fh)
        fh.__exit__ = Mock(return_value=False)
        fh.write.side_effect = content.write
        fh.name = "/targets/.gateway-target-fixture"
        fh.fileno.return_value = 9
        with patch.object(module.tempfile, "NamedTemporaryFile", return_value=fh), patch.object(module.os, "fsync") as fsync, patch.object(module.os, "chmod") as chmod, patch.object(module.os, "replace") as replace:
            self.assertTrue(module.sync(source, output, "gateway-245"))
            self.assertEqual(module.json.loads(content.getvalue())[0]["targets"], ["127.0.0.1:8782"])
            fsync.assert_called_once_with(9)
            chmod.assert_called_once_with(fh.name, 0o644)
            replace.assert_called_once_with(fh.name, str(output))


if __name__ == "__main__":
    unittest.main()
