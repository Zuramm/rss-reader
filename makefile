GO = go
GOFLAGS = --tags "fts"

TARGET = rss_reader

INSTALL = /usr/local/bin/install -c
INSTALL_PROGRAM = $(INSTALL)

prefix = /usr/local
exec_prefix = $(prefix)
bin_dir = $(exex_prefix)/bin

$(TARGET): mods
	$(GO) build $(GOFLAGS) -o $(TARGET) main.go

clean:
	rm -f $(TARGET)

run:
	$(GO) run $(GOFLAGS) main.go

mods:
	$(GO) mod download

install: $(TARGET)
	$(INSTALL) $(TARGET) $(DESTDIR)$(bindir)/$(TARGET)
