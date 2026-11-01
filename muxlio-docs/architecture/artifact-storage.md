# Artifact storage configuration

The coordinator uses its local content-addressed artifact directory by default.
To enable direct S3-compatible object transfers, set these environment variables
before starting `muxlio server start`:

```sh
export MUXLIO_S3_ENDPOINT=https://objects.example.test
export MUXLIO_S3_BUCKET=muxlio-artifacts
export MUXLIO_S3_ACCESS_KEY=...
export MUXLIO_S3_SECRET_KEY=...
export MUXLIO_S3_REGION=us-east-1 # optional; defaults to us-east-1
export MUXLIO_S3_SESSION_TOKEN=... # optional
export MUXLIO_S3_THRESHOLD_BYTES=5242880 # optional; defaults to 5 MiB
muxlio server start
```

Transfers at or above the threshold are written to S3 using path-style requests
and AWS Signature Version 4. Small artifacts remain in the local CAS. If an S3
upload fails, the artifact is saved locally; reads also fall back to local
storage. Consumers verify SHA-256 before bytes are returned. Keep credentials
in the process environment or a secret manager; they are not printed by the
artifact driver. S3-compatible services must support SigV4 and path-style
bucket access.
