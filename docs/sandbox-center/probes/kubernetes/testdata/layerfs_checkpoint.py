import argparse
import base64
import hashlib
import json
import os
import pathlib
import shutil
import stat
import tarfile
import tempfile
import time


TOOL_VERSION = "layerfs-probe-v1"
MAX_ARCHIVE_BYTES = 64 * 1024 * 1024
MAX_ENTRIES = 4096
OVERLAY_PREFIXES = ("trusted.overlay.", "user.overlay.")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest_file(filename):
    digest = hashlib.sha256()
    with open(filename, "rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return "sha256:" + digest.hexdigest()


def canonical_json(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode()


def xattrs(filename):
    return {
        name: os.getxattr(filename, name, follow_symlinks=False)
        for name in sorted(os.listxattr(filename, follow_symlinks=False))
    }


def entries(root):
    yield root
    for directory, directories, filenames in os.walk(root, followlinks=False, onerror=raise_walk_error):
        directories.sort()
        for name in sorted(directories + filenames):
            yield pathlib.Path(directory) / name


def raise_walk_error(error):
    raise error


def tree_digest(root):
    root = pathlib.Path(root)
    records = []
    links = {}
    for filename in sorted(entries(root)):
        info = filename.lstat()
        name = filename.relative_to(root).as_posix()
        attributes = {
            key: base64.b64encode(value).decode()
            for key, value in xattrs(filename).items()
            if not key.startswith(OVERLAY_PREFIXES)
        }
        value = None
        if stat.S_ISLNK(info.st_mode):
            value = os.readlink(filename)
        elif stat.S_ISREG(info.st_mode):
            value = digest_file(filename)
            if info.st_nlink > 1:
                inode = (info.st_dev, info.st_ino)
                value = [value, links.setdefault(inode, name)]
        records.append(
            [name, stat.S_IFMT(info.st_mode), stat.S_IMODE(info.st_mode),
             info.st_uid, info.st_gid, attributes, value]
        )
    return "sha256:" + hashlib.sha256(canonical_json(records)).hexdigest()


def write_marker(archive, name):
    marker = tarfile.TarInfo(name)
    marker.mode = 0
    archive.addfile(marker)


def write_layer(upper, filename):
    upper = pathlib.Path(upper)
    inodes = {}
    count = 0
    with tarfile.open(filename, "w", format=tarfile.PAX_FORMAT) as archive:
        for source in entries(upper):
            name = source.relative_to(upper).as_posix()
            info = source.lstat()
            attributes = xattrs(source)
            require(not source.name.startswith(".wh."), "reserved OCI whiteout name")
            require(not info.st_mode & (stat.S_ISUID | stat.S_ISGID),
                    "setuid/setgid files are outside probe coverage")
            overlay = {
                key: value for key, value in attributes.items()
                if key.startswith(OVERLAY_PREFIXES)
            }
            require(all(
                key in ("trusted.overlay.opaque", "trusted.overlay.whiteout",
                        "trusted.overlay.origin", "trusted.overlay.impure", "trusted.overlay.uuid")
                for key in overlay
            ), "unsupported overlay xattr: " + str(sorted(overlay)))
            if "trusted.overlay.uuid" in overlay:
                require(name == "." and len(overlay["trusted.overlay.uuid"]) == 16,
                        "invalid overlay instance uuid")
            opaque = overlay.get("trusted.overlay.opaque")
            require(opaque is None or (stat.S_ISDIR(info.st_mode) and opaque in (b"x", b"y")),
                    "invalid opaque directory")
            whiteout = stat.S_ISCHR(info.st_mode) and info.st_rdev == 0
            if "trusted.overlay.whiteout" in overlay:
                require(stat.S_ISREG(info.st_mode) and info.st_size == 0
                        and overlay["trusted.overlay.whiteout"] == b"",
                        "invalid xattr whiteout")
                whiteout = True
            require(all(key.startswith("user.") or key in overlay for key in attributes),
                    "unsupported non-user xattr")
            require(not stat.S_ISLNK(info.st_mode) or not attributes,
                    "symlink xattrs are outside probe coverage")
            count += 1 + (opaque == b"y")
            require(count <= MAX_ENTRIES, "too many layer entries")
            if whiteout:
                write_marker(archive, str(pathlib.PurePosixPath(name).with_name(".wh." + source.name)))
                continue
            require(stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode)
                    or stat.S_ISLNK(info.st_mode), "unsupported special file")
            header = archive.gettarinfo(str(source), arcname=name)
            header.uname = header.gname = ""
            if header.isreg() and info.st_nlink > 1:
                inode = (info.st_dev, info.st_ino)
                if inode in inodes:
                    header.type = tarfile.LNKTYPE
                    header.linkname = inodes[inode]
                    header.size = 0
                else:
                    inodes[inode] = name
            if not header.islnk():
                for key, value in attributes.items():
                    if key not in overlay:
                        try:
                            header.pax_headers["SCHILY.xattr." + key] = value.decode("utf-8")
                        except UnicodeDecodeError as error:
                            raise ValueError("user xattr must be UTF-8") from error
            require(archive.offset + header.size <= MAX_ARCHIVE_BYTES,
                    "layer exceeds probe size limit")
            if header.isreg():
                with source.open("rb") as data:
                    archive.addfile(header, data)
            else:
                archive.addfile(header)
            if opaque == b"y":
                write_marker(archive, str(pathlib.PurePosixPath(name) / ".wh..wh..opq"))
            require(archive.offset <= MAX_ARCHIVE_BYTES, "layer exceeds probe size limit")
    require(os.path.getsize(filename) <= MAX_ARCHIVE_BYTES, "layer exceeds probe size limit")


def pack(upper, store, base_digest, identity=None):
    require(base_digest.startswith("sha256:") and len(base_digest) == 71, "invalid base digest")
    store = pathlib.Path(store)
    store.mkdir(mode=0o700, parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".pack-", dir=store) as temporary:
        layer = pathlib.Path(temporary) / "layer.tar"
        write_layer(upper, layer)
        layer_digest = digest_file(layer)
        manifest = {
            "schemaVersion": 1,
            "toolVersion": TOOL_VERSION,
            "engine": "overlayfs",
            "baseLayerDigest": base_digest,
            "coverage": ["regularFiles", "directories", "binaryFiles", "permissions",
                         "ownership", "symlinks", "hardlinks", "utf8UserXattrs"],
            "consistency": "CrashConsistent",
            "createdAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "layer": {
                "mediaType": "application/vnd.oci.image.layer.v1.tar",
                "digest": layer_digest,
                "size": layer.stat().st_size,
            },
        }
        if identity is not None:
            manifest["saveAttempt"] = identity
        manifest_bytes = canonical_json(manifest)
        manifest_digest = "sha256:" + hashlib.sha256(manifest_bytes).hexdigest()
        os.replace(layer, store / layer_digest[7:])
        manifest_path = pathlib.Path(temporary) / "manifest.json"
        manifest_path.write_bytes(manifest_bytes)
        os.replace(manifest_path, store / manifest_digest[7:])
    return {"manifestDigest": manifest_digest, "layerDigest": layer_digest,
            "bytes": manifest["layer"]["size"]}


def digest_path(store, digest):
    require(isinstance(digest, str) and digest.startswith("sha256:")
            and len(digest) == 71 and all(char in "0123456789abcdef" for char in digest[7:]),
            "invalid object digest")
    return pathlib.Path(store) / digest[7:]


def validate_members(archive):
    members = []
    known = {}
    total_bytes = 0
    for member in archive:
        require(len(members) < MAX_ENTRIES, "too many layer entries")
        name = member.name.rstrip("/")
        parts = name.split("/")
        require(name == "." or (bool(name) and not name.startswith("/")
                and all(part not in ("", ".", "..") for part in parts)), "unsafe archive path")
        require(name not in known, "duplicate archive path")
        require(member.isdir() or member.isreg() or member.issym() or member.islnk(),
                "unsupported archive entry")
        require(not member.mode & 0o6000, "setuid/setgid archive entry")
        require(member.uid >= 0 and member.gid >= 0 and member.size >= 0, "invalid metadata")
        require(name != "." or member.isdir(), "root must be a directory")
        parent = pathlib.PurePosixPath(name).parent.as_posix()
        require(name == "." or (parent in known and known[parent].isdir()),
                "missing directory or symlink traversal in archive")
        require(not any(part.startswith(".wh.") for part in parts[:-1]),
                "whiteout used as parent")
        for key in member.pax_headers:
            if key.startswith("SCHILY.xattr."):
                attribute = key[len("SCHILY.xattr."):]
                require(attribute.startswith("user.") and not attribute.startswith("user.overlay.")
                        and not member.issym() and not member.islnk(),
                        "unsupported archive xattr")
            else:
                require(key in ("path", "linkpath", "size", "mtime"),
                        "unsupported archive PAX record")
        if parts[-1].startswith(".wh."):
            require(member.isreg() and member.size == 0 and set(member.pax_headers) <= {"path"},
                    "invalid whiteout marker")
            require(parts[-1] == ".wh..wh..opq"
                    or (len(parts[-1]) > 4 and not parts[-1][4:].startswith(".wh.")
                        and parts[-1][4:] not in (".", "..")),
                    "invalid whiteout target")
        elif member.islnk():
            require(member.linkname in known and known[member.linkname].isreg()
                    and not pathlib.PurePosixPath(member.linkname).name.startswith(".wh."),
                    "hardlink must reference an earlier regular file")
        total_bytes += member.size
        require(total_bytes <= MAX_ARCHIVE_BYTES, "expanded layer exceeds probe size limit")
        known[name] = member
        members.append(member)
    require("." in known, "missing root directory")
    for name in known:
        basename = pathlib.PurePosixPath(name).name
        if basename.startswith(".wh.") and basename != ".wh..wh..opq":
            target = str(pathlib.PurePosixPath(name).with_name(basename[4:]))
            require(target not in known, "whiteout conflicts with same-layer entry")
    return members


def apply_metadata(target, member):
    os.chown(target, member.uid, member.gid, follow_symlinks=False)
    if not member.issym():
        os.chmod(target, member.mode)
    os.utime(target, (member.mtime, member.mtime), follow_symlinks=False)
    for key, value in member.pax_headers.items():
        if key.startswith("SCHILY.xattr."):
            os.setxattr(target, key[len("SCHILY.xattr."):], value.encode("utf-8"),
                        follow_symlinks=False)


def read_checkpoint(store, manifest_digest, base_digest, identity=None):
    manifest_path = digest_path(store, manifest_digest)
    require(manifest_path.stat().st_size <= 65536, "manifest too large")
    manifest_bytes = manifest_path.read_bytes()
    require("sha256:" + hashlib.sha256(manifest_bytes).hexdigest() == manifest_digest,
            "manifest digest mismatch")
    manifest = json.loads(manifest_bytes)
    require(manifest["schemaVersion"] == 1 and manifest["toolVersion"] == TOOL_VERSION
            and manifest["engine"] == "overlayfs" and manifest["consistency"] == "CrashConsistent",
            "unsupported manifest")
    require(manifest["baseLayerDigest"] == base_digest, "base digest mismatch")
    if identity is not None:
        require(manifest.get("saveAttempt") == identity, "checkpoint belongs to another save attempt")
    layer = manifest["layer"]
    require(layer["mediaType"] == "application/vnd.oci.image.layer.v1.tar", "unsupported layer")
    layer_path = digest_path(store, layer["digest"])
    require(layer["size"] == layer_path.stat().st_size <= MAX_ARCHIVE_BYTES, "invalid layer size")
    require(digest_file(layer_path) == layer["digest"], "layer digest mismatch")
    return layer, layer_path


def save_attempt(upper, store, base_digest, identity):
    store = pathlib.Path(store)
    store.mkdir(mode=0o700, parents=True, exist_ok=False)
    packed = pack(upper, store, base_digest, identity)
    for digest in (packed["layerDigest"], packed["manifestDigest"]):
        with digest_path(store, digest).open("rb") as saved:
            os.fsync(saved.fileno())
    temporary = store / ".commit"
    with temporary.open("x", encoding="ascii") as marker:
        marker.write(packed["manifestDigest"])
        marker.flush()
        os.fsync(marker.fileno())
    os.rename(temporary, store / "commit")


def verify_attempt(store, base_digest, identity):
    store = pathlib.Path(store)
    try:
        with (store / "commit").open("rb") as marker:
            manifest_digest = marker.read(72).decode("ascii")
    except FileNotFoundError:
        try:
            store.stat()
        except FileNotFoundError:
            return "absent"
        return "incomplete"
    layer, layer_path = read_checkpoint(store, manifest_digest, base_digest, identity)
    with tarfile.open(layer_path, "r:") as archive:
        validate_members(archive)
    return "checkpoint: " + manifest_digest + ":" + layer["digest"][7:]


def restore(store, manifest_digest, base_digest, destination):
    layer, layer_path = read_checkpoint(store, manifest_digest, base_digest)
    destination = pathlib.Path(destination)
    require(not os.path.lexists(destination), "restore destination already exists")
    with tarfile.open(layer_path, "r:") as archive:
        members = validate_members(archive)
        staging = pathlib.Path(tempfile.mkdtemp(prefix=".restore-", dir=destination.parent))
        try:
            directories = []
            for member in members:
                target = staging / member.name
                basename = pathlib.PurePosixPath(member.name).name
                if basename == ".wh..wh..opq":
                    os.setxattr(target.parent, "trusted.overlay.opaque", b"y")
                    continue
                if basename.startswith(".wh."):
                    os.mknod(target.with_name(basename[4:]), stat.S_IFCHR, os.makedev(0, 0))
                    continue
                if member.isdir():
                    if member.name != ".":
                        target.mkdir(mode=0o700)
                    directories.append((target, member))
                    continue
                if member.issym():
                    os.symlink(member.linkname, target)
                elif member.islnk():
                    os.link(staging / member.linkname, target, follow_symlinks=False)
                else:
                    with archive.extractfile(member) as source, target.open("xb") as output:
                        shutil.copyfileobj(source, output)
                apply_metadata(target, member)
            for target, member in reversed(directories):
                apply_metadata(target, member)
            os.rename(staging, destination)
        finally:
            if staging.exists():
                shutil.rmtree(staging)
    return {"manifestDigest": manifest_digest, "layerDigest": layer["digest"]}


def main():
    parser = argparse.ArgumentParser()
    commands = parser.add_subparsers(dest="command", required=True)
    create = commands.add_parser("pack")
    create.add_argument("upper")
    create.add_argument("store")
    create.add_argument("base_digest")
    recover = commands.add_parser("restore")
    recover.add_argument("store")
    recover.add_argument("manifest_digest")
    recover.add_argument("base_digest")
    recover.add_argument("destination")
    digest = commands.add_parser("digest")
    digest.add_argument("root")
    save = commands.add_parser("save")
    save.add_argument("workspace")
    verify = commands.add_parser("verify")
    for attempt in (save, verify):
        attempt.add_argument("store")
        attempt.add_argument("base_digest")
        attempt.add_argument("sandbox")
        attempt.add_argument("generation", type=int)
        attempt.add_argument("attempt")
    args = parser.parse_args()
    if args.command == "pack":
        print(json.dumps(pack(args.upper, args.store, args.base_digest)))
    elif args.command == "restore":
        print(json.dumps(restore(args.store, args.manifest_digest, args.base_digest, args.destination)))
    elif args.command == "digest":
        print(tree_digest(args.root))
    else:
        identity = {"sandbox": args.sandbox, "generation": args.generation, "attempt": args.attempt}
        if args.command == "save":
            save_attempt(args.workspace, args.store, args.base_digest, identity)
        else:
            result = verify_attempt(args.store, args.base_digest, identity)
            pathlib.Path("/dev/termination-log").write_text(result, encoding="ascii")


if __name__ == "__main__":
    main()
