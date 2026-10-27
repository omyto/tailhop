# Third-party licensing audit

Tailhop's own [MIT license](../LICENSE) is unchanged. Distributions include
[THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES), copied from resolved upstream
sources rather than reconstructed license templates. This is an engineering
inventory, not a legal opinion or a declaration that all release checks passed.

## Scope and findings

The audit used Go 1.27.3, `go mod verify`, and the union of
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64/arm64 go list -deps -json ./cmd/tailhop`.
It found 36 third-party Go modules plus Go's standard library. The notice file
records their exact versions, upstream license/patent/author texts, applicable
nested license files, and copyright lines from selected source files. It includes
a conservative superset where linker dead-code elimination may discard packages.

The Go sources use BSD-2-Clause, BSD-3-Clause, MIT, ISC, and Apache-2.0 terms.
Tailscale itself is BSD-3-Clause; gVisor is Apache-2.0. klauspost/compress contains
multiple license sections, retained without deleting subcomponent terms. BSD,
MIT and ISC attribution/disclaimers are preserved; Apache license texts and any
applicable notices are retained, with no upstream source modifications. No MPL
or GPL license was found in the selected Go source/license files. That statement
does not extend to the Debian operating-system packages in Docker.

The full module graph is much larger than the compiled program. Platform-only
modules (for example Windows bindings), test dependencies such as go-cmp, and
Tailscale development tools are not all shipped merely because they occur in
the module graph or checksums. Builder tools and packages are not copied out of
the multi-stage Docker builder. Re-audit rather than assuming every module in
`go.sum` has the same attribution requirements.

## Outstanding browser-asset attribution

**Public redistribution is not yet cleared by this audit.** The exact amd64
binary retains JavaScript, CSS, and an Inter font from
`github.com/tailscale/web-client-prebuilt` at the version recorded in the notice
file. The linked bytes were checked, not inferred only from Go imports. The
htmx assets imported by eventbus were absent from that binary after linking.

The JavaScript contains React 18.2.0 and other license-bearing comments, which
are preserved. The font's own metadata identifies Inter Version 3.019,
commit `0a5106e0b`, and its copyright. The corresponding upstream SIL Open Font
License text is included. Tailscale's source yarn.lock was used to verify npm
archive integrity and collect 23 available runtime/CSS source-package license
texts. Build-only dependency trees were not treated as shipped JavaScript.

However, the prebuilt module does not identify the exact originating web-client
source commit. Its module-level BSD license is not enough evidence to relicense
all bundled third-party assets. Twenty-seven source-package archives omit full
license texts; their names and versions are listed under `UNRESOLVED WEB-CLIENT
SOURCE ATTRIBUTION` in the notices. These include Radix packages, wouter,
client-only, and two react utility packages. Requests for matching upstream
source licenses did not establish all required texts. Package metadata alone
was not turned into invented license grants or copyright holders.

Before a public binary/image release, establish the prebuilt asset's source
revision and complete its actual bundled dependency/license inventory, obtaining
missing texts from that source revision or upstream maintainers. Reconcile the
conservative source inventory with the built assets. Do not treat packaging
checks as evidence that this outstanding attribution question is resolved.

## Docker operating-system materials

The final image retains Debian's package copyright files under
`/usr/share/doc/*/copyright` and common licenses under
`/usr/share/common-licenses`; Tailhop's Dockerfile does not remove them.
Tailhop's LICENSE and notices are added under `/usr/share/doc/tailhop/`.
The base operating system includes separately licensed packages, including
copyleft components. Their corresponding-source requirements are independent
of the Go program's permissive licenses.

Before redistributing an actual image, record its digest and installed package
versions, inspect those copyright/source requirements, and retain or provide the
matching source through a license-compliant distribution arrangement. Debian
sources are published at [Debian's source archive](https://sources.debian.org/).
A generic source homepage is not a substitute for matching corresponding source
where a license requires it. Docker daemon access was unavailable during this
audit, so actual runtime-image contents and the matching source arrangement
remain unverified. See [Debian copyright policy](https://www.debian.org/doc/debian-policy/ch-docs.html#copyright-information).

## Updating notices

When Go, dependencies, build tags, architectures, or embedded assets change:

1. Run `go mod verify` and record the exact toolchain and module versions.
2. Run the non-test `go list -deps -json ./cmd/tailhop` inventory for both static
   Linux architectures. Compare with the built binary's `go version -m` output
   and retained embedded assets; distinguish test/build-only dependencies.
3. From the resolved module directories (`go list -m -json all`), copy applicable
   LICENSE/COPYING/NOTICE, copyright, author and patent files verbatim, including
   nested licenses and source-header copyright notices. Do not copy a different
   release's text or infer licenses from a module name. Go's bundled source also
   has its own license and vendored/subcomponent notices.
4. Audit embedded JavaScript/fonts separately using the exact source/bundle
   provenance. Verify archive integrity against upstream lockfiles; preserve
   their actual license notices and any source-availability obligations. Close
   the outstanding items above before calling the inventory complete.
5. Review the runtime operating-system package inventory and source obligations.
6. Run distribution tests and build both release archives. Confirm each contains
   LICENSE and THIRD_PARTY_NOTICES, and the Docker context/final image include
   both. Compare included bytes to source files; reject missing/corrupt copies.

No auditing tool or generator is needed at application runtime. Keep license
changes reviewable alongside dependency updates; do not change Tailhop's MIT
license to stand in for upstream license obligations.
