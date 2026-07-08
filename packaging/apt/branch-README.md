# Zester APT repository — pool of record

**This branch is generated. Do not edit or merge it by hand.**

It holds the durable state of the signed APT repository served at
`https://zester.cc/repo`:

- `pool/` — every published `.deb` (the only real state).
- `dists/` — regenerated + GPG-signed metadata (`Packages`, `Release`,
  `InRelease`, `Release.gpg`).
- `zester.gpg` / `zester.asc` — the exported public signing key.

It is updated by `.github/workflows/release.yml` on each `v*` tag and overlaid
into the GitHub Pages site by `.github/workflows/docs.yml`. See
[`packaging/apt/README.md`](https://github.com/nirnx/zester/blob/main/packaging/apt/README.md)
on `main` for how it all fits together.
