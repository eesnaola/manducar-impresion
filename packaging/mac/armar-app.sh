#!/bin/sh
# Arma «Manducar Impresión.app» a partir del binario de Mac y lo deja en un zip.
# Uso: packaging/mac/armar-app.sh <binario-darwin> <version> <salida.zip>
set -e
# El nombre del .app lleva tilde: el zip tiene que guardarlo en UTF-8 (bit 11)
# para que Archive Utility lo descomprima bien; con una locale UTF-8, zip lo hace.
export LC_ALL=C.UTF-8
bin=$1; version=$2; zipOut=$3
case $zipOut in /*) ;; *) zipOut=$PWD/$zipOut ;; esac
aqui=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
app="$tmp/Manducar Impresión.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
sed "s/__VERSION__/$version/g" "$aqui/Info.plist" > "$app/Contents/Info.plist"
# El ícono es el isotipo-placa de la marca (manducar.icns se genera del SVG
# de manducar: public/img/marca/isotipo-placa.svg; está commiteado porque el
# runner de Linux no tiene iconutil).
cp "$aqui/manducar.icns" "$app/Contents/Resources/manducar.icns"
cp "$bin" "$app/Contents/MacOS/manducar-impresion"
chmod 755 "$app/Contents/MacOS/manducar-impresion"
rm -f "$zipOut"
(cd "$tmp" && zip -q -r -y "$zipOut" "Manducar Impresión.app")
rm -rf "$tmp"
