"""Create a deterministic Load unpacked ZIP using only Python's standard library."""
import argparse
from pathlib import Path
import zipfile


def pack(dist, output):
    dist, output = Path(dist).resolve(), Path(output).resolve()
    if output.is_relative_to(dist):
        raise ValueError('Archive must be outside dist')
    if not (dist / 'manifest.json').is_file():
        raise ValueError('Build dist before packaging')
    paths = sorted(dist.rglob('*'))
    if any(path.is_symlink() for path in paths):
        raise ValueError('Symlinks are not allowed in the extension package')
    files = [path for path in paths if path.is_file()]
    output.parent.mkdir(parents=True, exist_ok=True)
    with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
        for path in files:
            entry = zipfile.ZipInfo(path.relative_to(dist).as_posix(), (1980, 1, 1, 0, 0, 0))
            entry.create_system = 3
            entry.external_attr = 0o100644 << 16
            entry.compress_type = zipfile.ZIP_DEFLATED
            archive.writestr(entry, path.read_bytes())
    with zipfile.ZipFile(output) as archive:
        if archive.testzip() is not None:
            raise ValueError('Archive integrity check failed')
        for path in files:
            if archive.read(path.relative_to(dist).as_posix()) != path.read_bytes():
                raise ValueError('Archive differs from dist')
    print(output)


if __name__ == '__main__':
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('output', nargs='?', type=Path, default=root.parents[1] / 'builds/igonotes-webclipper.zip')
    args = parser.parse_args()
    pack(root / 'dist', args.output)
