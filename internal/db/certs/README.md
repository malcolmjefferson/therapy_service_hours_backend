# RDS CA bundle

`us-west-2-bundle.pem` is AWS's **public** certificate bundle for RDS in us-west-2, embedded into the
Lambdas (`//go:embed` in `internal/db/pool.go`) so connections can verify the database server
(`sslmode=verify-full`, ADR-0001, SEC-43). It contains **certificates only, no private keys**, so it
is safe to commit.

| | |
|---|---|
| Source | https://truststore.pki.rds.amazonaws.com/us-west-2/us-west-2-bundle.pem |
| SHA-256 (2026-10-10) | `733b52e4b589586377e3e00f7c4625b61e14cbf0b5e08cace28b4f8aecc27c66` |
| Contents | Amazon RDS us-west-2 Root CA **ECC384 G1** (expires 2121), **RSA4096 G1** (2121), **RSA2048 G1** (2061) |

## Refresh (yearly runbook check, ADR-0001 open item)

```bash
curl -sSf -o /tmp/bundle.pem https://truststore.pki.rds.amazonaws.com/us-west-2/us-west-2-bundle.pem
sha256sum /tmp/bundle.pem internal/db/certs/us-west-2-bundle.pem
```

If the hashes differ (AWS added or rotated a CA), copy the new file here, run the tests, update the
hash above, and release before the RDS instance switches to the new CA.
