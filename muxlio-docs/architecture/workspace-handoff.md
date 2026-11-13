# Prepared workspace handoff

A client can launch a run from a laptop against a prepared workspace on another
machine. Workspace IDs authorize a directory and its owning worker; an optional
`workspaceDigest` in the plan pins its contents independently of host paths.
The workspace can hold documents, datasets, source files, or any other files.
This mechanism uses operator-provisioned directories, without requiring Git.

1. Prepare the input directory on the laptop. For a Git project, check out the
   desired commit into a dedicated worktree first. Include every dependency and
   input file the workflow needs.
2. Run `muxlio workspace digest /absolute/prepared/input` to obtain its
   `sha256:...` content identity.
3. Transfer those contents to a dedicated directory on the Mac mini, using your
   provisioning mechanism. Re-run the digest command there and verify equality.
4. Register that identity with the coordinator and the owning worker. For example,
   in each runtime's `config.json`:

   ```json
   {
     "workspaces": {
       "report-input-v1": {
         "workerId": "mac-mini",
         "path": "/Users/operator/prepared/report-input-v1",
         "digest": "sha256:<64 lowercase hex characters>"
       }
     }
   }
   ```

   The path is local to the owning worker. The coordinator does not open a remote
   path. Restart the services after configuring the binding. The worker verifies
   the directory and digest before advertising its workspace ID and digest labels.
5. Set both `workspaceId: "report-input-v1"` and `workspaceDigest: "sha256:..."`
   in the workflow options (both SDK authoring APIs support these fields), or in
   compiled Plan IR. Submit it to the coordinator from the laptop. Omit a
   `workspace` path input; the ID selects the authorized worker-local directory.

The pin is part of the immutable plan digest and the durable execute request.
The coordinator rejects missing or conflicting plan/binding pins and workers
advertising another content revision. Dispatch requires the owning worker and
matching digest. The worker repeats directory and content checks before execution;
default snapshot mode also verifies the copied attempt contents before launching
the executor. Missing directories, changed contents, and mismatched revisions
fail before executor work, without copying a default checkout or fetching a branch.
Use a new content pin when changing inputs. Unpinned legacy bindings keep their
existing behavior; a binding with a digest requires an explicit matching plan pin.

The version 1 digest hashes a sorted tree of relative paths, directory entries,
file SHA-256 hashes, owner executable bits, and relative symlink targets. File
timestamps, ownership, other permission bits, and absolute host paths do not
affect identity. `.git` entries at any depth are omitted, matching attempt
snapshots. Non-regular files, dangling symlinks, links escaping the workspace,
and non-UTF-8 paths or symlink targets are rejected. File contents may contain
arbitrary bytes. Git commit IDs, repository origins, and Git metadata are not part
of this content identity: different commits with identical prepared contents have
the same digest. Changes to tracked, untracked, or ignored files are included.

Keep prepared inputs unchanged while runs use them. Snapshot mode gives each
attempt independent writable files. `shared-write` can modify the bound directory,
so a later attempt will reject it if its content revision has changed. Content
checks attest inputs at verification time; they do not lock out external writers.
Read-only remote polling and reconciliation do not require the original inputs.

Environment manifests pin execution software and permissions separately; they
do not transfer workspace files. Existing content-addressed artifact inputs can
carry individual files across hosts with digest verification. Workspace handoff
does not automatically download or unpack archives, clone repositories, install
dependencies, or grant authority to an artifact-provided path. Operators may use
artifacts to provision a prepared directory and then register its tree digest.
