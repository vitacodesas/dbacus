#!/usr/bin/env bash
# Regenera los iconos de la app desde build/appicon.svg (fuente editable):
#   build/appicon.png (1024 px, también lo usa Linux), build/darwin/icons.icns y build/windows/icon.ico.
# En macOS además recompila build/darwin/Assets.car desde build/appicon.icon (necesita actool, de Xcode): es el
# catálogo que macOS 26 usa por encima del .icns. La capa de ese paquete (build/appicon.icon/Assets/dbacus_layer.svg)
# repite el dibujo sin fondo ni sombra: si cambias el ábaco en appicon.svg, cámbialo también allí.
# Requiere `cd frontend && npm ci` (usa el Chromium de Playwright para rasterizar) y wails3.
set -euo pipefail
cd "$(dirname "$0")/.."

node - <<'JS'
const { chromium } = require("./frontend/node_modules/@playwright/test");
const fs = require("fs");
(async () => {
  const svg = fs.readFileSync("build/appicon.svg", "utf8");
  const b = await chromium.launch();
  const p = await b.newPage({ viewport: { width: 1024, height: 1024 } });
  await p.setContent(`<html><body style="margin:0;background:transparent">${svg}</body></html>`);
  await p.screenshot({ path: "build/appicon.png", omitBackground: true });
  await b.close();
})();
JS

args=(-input build/appicon.png -macfilename build/darwin/icons.icns -windowsfilename build/windows/icon.ico)
if [ "$(uname)" = Darwin ]; then
  args+=(-iconcomposerinput build/appicon.icon -macassetdir build/darwin)
fi
wails3 generate icons "${args[@]}"
