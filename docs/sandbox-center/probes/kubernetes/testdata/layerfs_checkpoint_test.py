import copy
import hashlib
import json
import os
import pathlib
import stat
import tarfile
import tempfile
import unittest
from unittest import mock

import layerfs_checkpoint as checkpoint


class CheckpointTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)
        self.upper = self.root / "source"
        self.upper.mkdir()
        self.store = self.root / "objects"
        self.destination = self.root / "restored"
        self.base_digest = "sha256:" + "1" * 64

    def create(self):
        return checkpoint.pack(self.upper, self.store, self.base_digest)

    def restore(self, result, base_digest=None):
        return checkpoint.restore(
            self.store, result["manifestDigest"],
            self.base_digest if base_digest is None else base_digest,
            self.destination,
        )

    def assert_rejected(self, result, message, base_digest=None):
        with self.assertRaisesRegex(ValueError, message):
            self.restore(result, base_digest)
        self.assertFalse(os.path.lexists(self.destination))
        self.assertEqual(list(self.root.glob(".restore-*")), [])

    def install_archive(self, headers):
        result = self.create()
        manifest = json.loads(checkpoint.digest_path(self.store, result["manifestDigest"]).read_bytes())
        archive_path = self.root / "input.tar"
        with tarfile.open(archive_path, "w", format=tarfile.PAX_FORMAT) as archive:
            for header in headers:
                archive.addfile(header)
        layer_digest = checkpoint.digest_file(archive_path)
        os.replace(archive_path, checkpoint.digest_path(self.store, layer_digest))
        manifest["layer"]["digest"] = layer_digest
        manifest["layer"]["size"] = checkpoint.digest_path(self.store, layer_digest).stat().st_size
        content = checkpoint.canonical_json(manifest)
        manifest_digest = "sha256:" + hashlib.sha256(content).hexdigest()
        checkpoint.digest_path(self.store, manifest_digest).write_bytes(content)
        return {"manifestDigest": manifest_digest, "layerDigest": layer_digest}

    def test_regular_files_links_modes_and_root_metadata(self):
        (self.upper / "nested").mkdir()
        (self.upper / "nested" / "binary").write_bytes(bytes([0, 255, 128, 10]))
        (self.upper / "nested" / "binary").chmod(0o700)
        os.link(self.upper / "nested" / "binary", self.upper / "hardlink")
        (self.upper / "link").symlink_to("nested/binary")
        (self.upper / "escape").symlink_to("/not-exposed/private")
        self.upper.chmod(0o750)
        os.utime(self.upper / "nested" / "binary", (1700000000, 1700000000))
        before = checkpoint.tree_digest(self.upper)
        result = self.create()
        source_file = self.upper / "nested" / "binary"
        source_file.write_bytes(b"destroyed after packing")
        self.restore(result)
        self.assertEqual(before, checkpoint.tree_digest(self.destination))
        self.assertEqual(os.stat(self.destination / "hardlink").st_ino,
                         os.stat(self.destination / "nested" / "binary").st_ino)
        self.assertEqual(os.readlink(self.destination / "escape"), "/not-exposed/private")
        self.assertEqual((self.destination / "nested" / "binary").stat().st_mtime, 1700000000)

    def test_manifest_and_layer_integrity_before_restore(self):
        for target in ("manifestDigest", "layerDigest"):
            with self.subTest(target=target):
                result = self.create()
                filename = checkpoint.digest_path(self.store, result[target])
                content = bytearray(filename.read_bytes())
                content[0] ^= 1
                filename.write_bytes(content)
                self.assert_rejected(result, "digest mismatch")
                filename.unlink()

    def test_wrong_base_is_rejected(self):
        self.assert_rejected(self.create(), "base digest mismatch", "sha256:" + "2" * 64)

    def test_existing_destination_is_not_overwritten(self):
        result = self.create()
        self.destination.mkdir()
        (self.destination / "retained").write_text("original")
        with self.assertRaisesRegex(ValueError, "destination already exists"):
            self.restore(result)
        self.assertEqual((self.destination / "retained").read_text(), "original")

    def test_pack_rejects_reserved_names_and_special_files(self):
        for name, kind in ((".wh.user-file", "regular"), ("pipe", "fifo")):
            with self.subTest(name=name):
                filename = self.upper / name
                if kind == "fifo":
                    os.mkfifo(filename)
                else:
                    filename.write_text("data")
                with self.assertRaisesRegex(ValueError, "reserved|special"):
                    self.create()
                filename.unlink()
                self.assertEqual(list(self.store.iterdir()), [])

    def test_failed_restore_removes_staging(self):
        (self.upper / "file").write_text("retained")
        result = self.create()
        with mock.patch.object(checkpoint, "apply_metadata", side_effect=OSError("storage failure")):
            with self.assertRaisesRegex(OSError, "storage failure"):
                self.restore(result)
        self.assertFalse(self.destination.exists())
        self.assertEqual(list(self.root.glob(".restore-*")), [])

    def test_failed_final_rename_removes_written_files(self):
        for name in ("first", "second"):
            (self.upper / name).write_text(name)
        result = self.create()
        with mock.patch.object(checkpoint.os, "rename", side_effect=OSError("rename failure")):
            with self.assertRaisesRegex(OSError, "rename failure"):
                self.restore(result)
        self.assertFalse(self.destination.exists())
        self.assertEqual(list(self.root.glob(".restore-*")), [])

    def test_scandir_failure_is_never_a_successful_snapshot(self):
        blocked = self.upper / "blocked"
        blocked.mkdir()
        (blocked / "important").write_text("must not disappear")
        original_scandir = os.scandir

        def fail_scandir(filename):
            if not isinstance(filename, int) and pathlib.Path(filename) == blocked:
                raise OSError("scan failure")
            return original_scandir(filename)

        with mock.patch.object(checkpoint.os, "scandir", side_effect=fail_scandir):
            with self.assertRaisesRegex(OSError, "scan failure"):
                self.create()
            with self.assertRaisesRegex(OSError, "scan failure"):
                checkpoint.tree_digest(self.upper)
        self.assertEqual(list(self.store.iterdir()), [])

    def test_user_xattrs_round_trip_and_binary_rejection(self):
        filename = self.upper / "file"
        filename.write_text("contents")
        os.setxattr(filename, "user.notes", "中文 metadata".encode())
        result = self.create()
        self.restore(result)
        self.assertEqual(os.getxattr(self.destination / "file", "user.notes"),
                         "中文 metadata".encode())
        os.setxattr(filename, "user.notes", b"\xff\x00")
        before = set(self.store.iterdir())
        with self.assertRaisesRegex(ValueError, "xattr must be UTF-8"):
            self.create()
        self.assertEqual(set(self.store.iterdir()), before)

    def test_unicode_and_long_whiteouts_survive_pax_serialization(self):
        os.mknod(self.upper / "中文删除", stat.S_IFCHR, os.makedev(0, 0))
        for name in ("中文目录", "long-" + "a" * 110):
            directory = self.upper / name
            directory.mkdir()
            (directory / "new").write_text("replacement")
            os.setxattr(directory, "trusted.overlay.opaque", b"y")
        result = self.create()
        self.restore(result)
        deleted = (self.destination / "中文删除").lstat()
        self.assertTrue(stat.S_ISCHR(deleted.st_mode) and deleted.st_rdev == 0)
        for name in ("中文目录", "long-" + "a" * 110):
            self.assertEqual(os.getxattr(self.destination / name, "trusted.overlay.opaque"), b"y")
            self.assertEqual((self.destination / name / "new").read_text(), "replacement")

    def test_malicious_archives_are_rejected_before_materialization(self):
        sentinel = self.root / "outside"
        sentinel.write_text("retained")
        symlink = tarfile.TarInfo("link")
        symlink.type, symlink.linkname = tarfile.SYMTYPE, str(self.root)
        hardlink = tarfile.TarInfo("alias")
        hardlink.type, hardlink.linkname = tarfile.LNKTYPE, "../outside"
        pax_traversal = tarfile.TarInfo("safe")
        pax_traversal.pax_headers = {"path": "../outside"}
        cases = {
            "traversal": [tarfile.TarInfo("../outside")],
            "pax traversal": [pax_traversal],
            "symlink parent": [symlink, tarfile.TarInfo("link/outside")],
            "hardlink": [hardlink],
            "whiteout conflict": [tarfile.TarInfo(".wh.file"), tarfile.TarInfo("file")],
        }
        for label, headers in cases.items():
            with self.subTest(label=label):
                root = tarfile.TarInfo(".")
                root.type = tarfile.DIRTYPE
                result = self.install_archive([root] + headers)
                self.assert_rejected(result, "unsafe|traversal|hardlink|conflicts")
                self.assertEqual(sentinel.read_text(), "retained")

    def test_archive_validation(self):
        directory = tarfile.TarInfo(".")
        directory.type = tarfile.DIRTYPE

        def entry(name, kind=tarfile.REGTYPE, link=""):
            header = tarfile.TarInfo(name)
            header.type, header.linkname = kind, link
            return header

        privileged = entry("privileged")
        privileged.mode = stat.S_ISUID | 0o755
        kernel_xattr = entry("kernel-xattr")
        kernel_xattr.pax_headers = {"SCHILY.xattr.trusted.overlay.metacopy": "y"}
        cases = {
            "parent traversal": [entry("../outside")],
            "absolute path": [entry("/outside")],
            "dot path": [entry("./outside")],
            "empty component": [entry("nested//file")],
            "missing parent": [entry("missing/file")],
            "symlink parent": [entry("link", tarfile.SYMTYPE, "/tmp"), entry("link/file")],
            "hardlink to symlink": [entry("link", tarfile.SYMTYPE, "/tmp"),
                                    entry("alias", tarfile.LNKTYPE, "link")],
            "hardlink escape": [entry("alias", tarfile.LNKTYPE, "../outside")],
            "hardlink to whiteout": [entry(".wh.file"), entry("alias", tarfile.LNKTYPE, ".wh.file")],
            "duplicate": [entry("same"), entry("same")],
            "device": [entry("device", tarfile.CHRTYPE)],
            "invalid whiteout": [entry(".wh.")],
            "whiteout dot target": [entry(".wh...")],
            "whiteout parent": [entry(".wh.dir", tarfile.DIRTYPE)],
            "whiteout conflict": [entry(".wh.file"), entry("file")],
            "setuid": [privileged],
            "overlay xattr injection": [kernel_xattr],
            "root symlink": [entry(".", tarfile.SYMTYPE, "/tmp")],
        }
        for label, headers in cases.items():
            with self.subTest(label=label):
                with self.assertRaises(ValueError):
                    checkpoint.validate_members([copy.copy(directory)] + headers)

    def test_limits(self):
        (self.upper / "large").write_bytes(b"data")
        with mock.patch.object(checkpoint, "MAX_ARCHIVE_BYTES", 2):
            with self.assertRaisesRegex(ValueError, "size limit"):
                self.create()
        with mock.patch.object(checkpoint, "MAX_ENTRIES", 1):
            with self.assertRaisesRegex(ValueError, "too many"):
                self.create()

    def test_manifest_records_coverage_and_explicit_consistency(self):
        result = self.create()
        manifest = json.loads(checkpoint.digest_path(self.store, result["manifestDigest"]).read_bytes())
        self.assertEqual(manifest["consistency"], "CrashConsistent")
        self.assertEqual(manifest["baseLayerDigest"], self.base_digest)
        self.assertIn("utf8UserXattrs", manifest["coverage"])
        self.assertEqual(manifest["toolVersion"], checkpoint.TOOL_VERSION)

    def test_opaque_marker_does_not_depend_on_entry_order(self):
        for index, names in enumerate(([".wh..wh..opq", "new"], ["new", ".wh..wh..opq"])):
            with self.subTest(names=names):
                root = tarfile.TarInfo(".")
                root.type = tarfile.DIRTYPE
                root.mode = 0o750
                headers = [root] + [tarfile.TarInfo(name) for name in names]
                result = self.install_archive(headers)
                self.destination = self.root / ("restored-" + str(index))
                self.restore(result)
                self.assertEqual(os.getxattr(self.destination, "trusted.overlay.opaque"), b"y")
                self.assertTrue((self.destination / "new").is_file())


if __name__ == "__main__":
    unittest.main(verbosity=2)
