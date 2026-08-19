VERSION ?= dev
TARGETS := windows-amd64 darwin-arm64 linux-amd64
REPO := https://github.com/eesnaola/manducar-impresion

.PHONY: build test release-notes clean

test:
	go test -race ./... && go vet ./... && GOOS=windows GOARCH=amd64 go vet ./...

build:
	@mkdir -p dist
	@for t in $(TARGETS); do \
	  os=$${t%-*}; arch=$${t#*-}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "-s -w -X main.Version=$(VERSION)" -o dist/manducar-impresion-$$t$$ext . ; \
	done
	@cd dist && shasum -a 256 manducar-impresion-* > SHA256SUMS

# El bloque para pegar en config/packages/printing.yaml de manducar, con los
# hashes de ESTA build. Ojo: para publicar de verdad va el bloque que arma el
# release de GitHub, que son los binarios que la gente se baja.
release-notes:
	@echo "# Ojo: estos son los hashes de tu build local. Para publicar, usá" >&2
	@echo "# el bloque que aparece en el cuerpo del release de GitHub." >&2
	@echo "    printing.agent.version: '$(VERSION)'"
	@echo "    printing.agent.downloads:"
	@for t in $(TARGETS); do \
	  os=$${t%-*}; ext=""; [ "$$os" = windows ] && ext=".exe"; \
	  f=manducar-impresion-$$t$$ext; h=$$(grep " $$f$$" dist/SHA256SUMS | cut -d' ' -f1); \
	  echo "        $$t:"; \
	  echo "            url: '$(REPO)/releases/download/v$(VERSION)/$$f'"; \
	  echo "            sha256: '$$h'"; \
	done

clean:
	rm -rf dist
