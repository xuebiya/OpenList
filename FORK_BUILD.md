# Fork build

Frontend repository: https://github.com/xuebiya/OpenList-Frontend

`frontend-ref.txt` pins the full frontend commit. Update it deliberately to rebuild newer frontend source. Every push to main builds the pinned frontend and 50 independent CGO targets. Releases contain one zip (Windows) or tar.gz (other platforms) and SHA-256 file per target. Failures publish an explicitly marked prerelease; never imply full success.

Upstream workflows are archived under `.github/upstream-workflows/` and do not execute. Legacy build.sh defaults to xuebiya frontend latest assets. Go module paths remain unchanged for import compatibility.
