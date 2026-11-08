# Build & deploy nasd.  `make deploy` builds everything and installs it in
# the dev VM.  Later: `make deploy NAS=nas@192.168.1.50` for the real box.
export PATH := $(HOME)/.local/go/bin:$(PATH)

BIN := bin/nasd
# Target: the dev VM by default; the real NAS with `make deploy NAS=nas@nas.local`
# (key created by image/build.sh).
ifdef NAS
SSH := ssh -i image/id_ed25519 -o IdentitiesOnly=yes $(NAS)
else
SSH := vm/ssh.sh
endif

.PHONY: all ui build test deploy logs vm-reset

all: build

ui:
	cd web && npm install --silent && npm run build

# CGO_ENABLED=0: a static binary with no libc dependency.
# GOARCH=amd64: the Atom D410 is 64-bit; GOAMD64=v1 keeps it compatible
# with that old CPU (no AVX instructions).
build: ui
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOAMD64=v1 go build -trimpath -ldflags "-s -w -X main.version=$$(git describe --always --dirty)" -o $(BIN) ./cmd/nasd
	@ls -lh $(BIN)

test:
	go vet ./...
	go test ./...

deploy: build
	cat $(BIN) | $(SSH) 'sudo tee /usr/local/bin/nasd.new >/dev/null && sudo chmod 755 /usr/local/bin/nasd.new && sudo mv /usr/local/bin/nasd.new /usr/local/bin/nasd'
	cat deploy/nasd.service | $(SSH) 'sudo tee /etc/systemd/system/nasd.service >/dev/null'
	$(SSH) 'sudo systemctl daemon-reload && sudo systemctl enable --quiet nasd && sudo systemctl restart nasd && systemctl is-active nasd'
	@echo "UI: https://$(if $(NAS),$(lastword $(subst @, ,$(NAS))),localhost):8443"

logs:
	$(SSH) 'journalctl -u nasd -f -n 50'

vm-reset:
	vm/setup.sh
