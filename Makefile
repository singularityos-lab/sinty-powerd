BIN := sinty-powerd
PREFIX ?= /usr

.PHONY: all build static test vet install clean

all: build

build:
	go build -o $(BIN) ./cmd/sinty-powerd

static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/sinty-powerd

test:
	go test ./...

vet:
	go vet ./...

install: static
	install -Dm755 $(BIN) $(DESTDIR)$(PREFIX)/libexec/$(BIN)
	install -Dm644 packaging/sinty-powerd.service $(DESTDIR)$(PREFIX)/lib/systemd/system/sinty-powerd.service
	install -Dm644 packaging/sinty-powerd.conf $(DESTDIR)$(PREFIX)/share/dbus-1/system.d/sinty-powerd.conf
	for n in org.freedesktop.UPower org.freedesktop.UPower.PowerProfiles net.hadess.PowerProfiles; do \
		install -Dm644 packaging/$$n.service $(DESTDIR)$(PREFIX)/share/dbus-1/system-services/$$n.service; \
	done

clean:
	rm -f $(BIN)
