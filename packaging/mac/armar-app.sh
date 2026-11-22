#!/bin/sh
# Arma «Manducar Impresión.app» a partir del binario de Mac, lo sella y lo deja
# en un zip. Corre en una Mac: el sello lo pone codesign, que sólo existe ahí.
# Uso: packaging/mac/armar-app.sh <binario-darwin> <version> <salida.zip>
set -e
command -v codesign >/dev/null || {
	echo "armar-app.sh: falta codesign. El .app se arma en una Mac: sin sellar, a quien lo baja macOS le dice que está dañado." >&2
	exit 1
}
# El nombre del .app lleva tilde: el zip tiene que guardarlo en UTF-8 (bit 11)
# para que Archive Utility lo descomprima bien; con una locale UTF-8, zip lo hace.
export LC_ALL=C.UTF-8
bin=$1; version=$2; zipOut=$3

# macOS 15 decide si un programa puede hablar con la red del local (el permiso
# de «Red local») identificándolo por el LC_UUID del ejecutable, y a un agente
# de launchd —como queda éste al instalarse— el permiso le aplica. Go 1.23 no
# lo pone salvo que se compile con `-ldflags "-B gobuildid"`. Sin él la
# impresora de red da «no hay camino hasta esa dirección» con la red sana: no
# se empaqueta.
otool -l "$bin" | grep -q LC_UUID || {
	echo "armar-app.sh: $bin no trae LC_UUID. Compilalo con -ldflags \"-B gobuildid\": sin ese identificador, macOS 15 puede dejar al agente sin llegar a las impresoras de red." >&2
	exit 1
}
case $zipOut in /*) ;; *) zipOut=$PWD/$zipOut ;; esac
aqui=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
app="$tmp/Manducar Impresión.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
sed "s/__VERSION__/$version/g" "$aqui/Info.plist" > "$app/Contents/Info.plist"
# El ícono es el isotipo-placa de la marca (manducar.icns se genera del SVG
# de manducar: public/img/marca/isotipo-placa.svg, y está commiteado).
cp "$aqui/manducar.icns" "$app/Contents/Resources/manducar.icns"
cp "$bin" "$app/Contents/MacOS/manducar-impresion"
chmod 755 "$app/Contents/MacOS/manducar-impresion"
# El sello. El binario de Go ya sale firmado por el linker, pero como
# ejecutable suelto: adentro de un .app esa firma promete un Info.plist y unos
# recursos que no selló, macOS la da por rota, y al que bajó el zip le dice
# «está dañado y no puede abrirse», sin ninguna salida desde la pantalla. Con
# el paquete sellado (firma ad hoc: no hay certificado) el aviso es «Apple no
# pudo verificar…», que se pasa desde Privacidad y seguridad.
codesign --force --sign - "$app"
rm -f "$zipOut"
(cd "$tmp" && zip -q -r -y "$zipOut" "Manducar Impresión.app")
# Se publica el zip, así que el sello se mira en lo que sale de él: un zip que
# pierde un archivo o le cambia el nombre lo rompe igual que no haber sellado.
mkdir "$tmp/control"
ditto -x -k "$zipOut" "$tmp/control"
codesign --verify --deep --strict "$tmp/control/Manducar Impresión.app"
