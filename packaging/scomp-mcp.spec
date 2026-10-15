%global debug_package %{nil}

Name:           scomp-mcp
Version:        0.1.0
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
* Thu Oct 15 2026 smart-bit.me <admin@scomp.me> - 0.1.0-1
- Initial package for the browser-approved, scoped MCP terminal client.
