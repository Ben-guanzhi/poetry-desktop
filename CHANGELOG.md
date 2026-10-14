# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed
- Three datasets (五代诗词, 花间集, 南唐二主词) shared the same directory, causing data corruption
- "清空收藏" only cleared the visible page (1,000 poems) instead of all collections
- Search hardcoded navigation to 维基文库 page instead of working from any page
- Version was injected but never displayed (About panel hardcoded v1.0)
- ~23 store errors were silently discarded instead of surfaced to the user

### Added
- ClearAllCollections store method for atomic collection clearing
- Version field wired through AppUI from build-time ldflags

## [1.0.0] - 2026-10-13

### Added
- Initial release
- Go application with Fyne GUI toolkit
- Desktop poetry management interface
