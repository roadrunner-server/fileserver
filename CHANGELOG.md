# Changelog

## [6.1.0](https://github.com/roadrunner-server/fileserver/compare/v6.0.0...v6.1.0) (2026-10-27)

### Changed

* Use Fiber v3.6.1 for file serving. Match URL prefixes at path-segment boundaries. ([#107](https://github.com/roadrunner-server/fileserver/pull/107))
* Update the Go toolchain to 1.27.2.
* Update RoadRunner config to v6.0.0, errors to v1.6.0, and tcplisten to v1.6.1.
* Update indirect Go dependencies, including fasthttp to v1.77.0 and klauspost/compress to v1.20.3.

### CI

* Add Release Please automation. ([#101](https://github.com/roadrunner-server/fileserver/pull/101))
* Update the GitHub App token action to v3.3.0. ([#104](https://github.com/roadrunner-server/fileserver/pull/104))
* Use the CodeQL v4 actions.
