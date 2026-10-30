%global debug_package %{nil}

Name:           scomp-mcp
Version:        0.2.0
Release:        1%{?dist}
Summary:        Scomp Model Context Protocol terminal client
License:        AGPL-3.0-or-later
URL:            https://github.com/smart-bit-me/scomp-mcp

Source0:        {{{ git_repo_pack }}}
Source1:        {{{ go_vendor_pack }}}

BuildRequires:  golang >= 1.25

%description
scomp-mcp is a local stdio Model Context Protocol client for access to paired
scomp terminal hosts. It uses browser-approved, scoped credentials and installs
only the client executable; it does not run a system service.

%prep
%autosetup -n %{name}
tar xzf %{SOURCE1}

%build
export CGO_ENABLED=0
export GOFLAGS="-mod=vendor"
go build -trimpath -ldflags="-s -w -X main.version=%{version}-%{release}" -o scomp-mcp ./cmd/scomp-mcp

%check
export CGO_ENABLED=0
export GOFLAGS="-mod=vendor"
go test ./...

%install
install -Dm755 scomp-mcp %{buildroot}%{_bindir}/scomp-mcp

%files
%license LICENSE
%doc README.md SECURITY.md docs/local-e2e.md
%{_bindir}/scomp-mcp

%changelog
* Fri Oct 30 2026 smart-bit.me <admin@scomp.me> - 0.2.0-1
- Return short text summaries beside structured results; add serve --json-text
  for clients that read only text content.
- Compact scomp_read_terminal results; raise the long-poll maximum to 50 s.
- Add explicit tail resync (resync/start, tail_bytes) that always reports
  skipped_bytes, and a bounded literal-marker wait (until).
- Reconnect with backoff for 60 s, report disconnect_reason, and accept 2 MiB
  frames.
- Ask the user to type the login code; treat HTTP 429 as slow_down.
- Strip DCS/SOS/PM/APC strings, charset escapes, and C1 controls from output.
* Thu Oct 15 2026 smart-bit.me <admin@scomp.me> - 0.1.0-1
- Initial package for the browser-approved, scoped MCP terminal client.
