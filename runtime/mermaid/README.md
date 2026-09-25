# Official Mermaid runtime

This optional runtime supplies the upstream Mermaid parser and SVG/PNG renderer.
lazymermaid ships its small JSON-lines bridge inside the Go binary; this directory
only installs its JavaScript dependencies and Puppeteer's compatible Chrome.

```sh
npm ci --prefix runtime/mermaid
npm exec --prefix runtime/mermaid -- puppeteer browsers install chrome
```

Point lazymermaid's renderer runtime directory at this directory. Installation is
explicit; starting lazymermaid never installs packages or downloads a browser.
The explicit browser command also supports npm versions that disable dependency
installation scripts by default.
The first render starts a local browser; subsequent requests reuse its page.
Validation and image rendering always use the same Mermaid installation.

Versions are pinned in `package-lock.json`. Upgrade Mermaid together with the
offline handbook snapshot and rerun the renderer integration tests. The bridge
serves package assets on a private loopback port and blocks external browser
requests. Syntax errors retain upstream messages; upstream line numbers are
marked unreliable because Mermaid preprocessing can change source coordinates.
