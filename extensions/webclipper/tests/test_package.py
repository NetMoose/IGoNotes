"""The release ZIP must contain exactly the loadable dist, with stable bytes."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
import zipfile

spec = importlib.util.spec_from_file_location('clipper_package', Path(__file__).parents[1] / 'scripts/package.py')
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


class PackageTests(unittest.TestCase):
    def test_roundtrip_is_exact_and_deterministic(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / 'dist'
            (dist / 'LICENSES').mkdir(parents=True)
            for name, body in {'manifest.json': b'{"manifest_version":3}', 'editor.html': b'<html></html>', 'LICENSES/license.txt': b'license'}.items():
                (dist / name).write_bytes(body)
            first, second = root / 'first.zip', root / 'second.zip'
            package.pack(dist, first)
            (dist / 'editor.html').touch()
            package.pack(dist, second)
            self.assertEqual(first.read_bytes(), second.read_bytes())
            with zipfile.ZipFile(first) as archive:
                self.assertEqual(archive.namelist(), ['LICENSES/license.txt', 'editor.html', 'manifest.json'])
                for name in archive.namelist():
                    self.assertEqual(archive.read(name), (dist / name).read_bytes())

    def test_symlinks_and_output_inside_dist_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            dist = root / 'dist'
            dist.mkdir()
            (dist / 'manifest.json').write_text('{}')
            with self.assertRaises(ValueError):
                package.pack(dist, dist / 'archive.zip')
            (root / 'private').write_text('do not package')
            (dist / 'link').symlink_to(root / 'private')
            with self.assertRaises(ValueError):
                package.pack(dist, root / 'archive.zip')


if __name__ == '__main__':
    unittest.main()
