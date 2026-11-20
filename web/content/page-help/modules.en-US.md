# Module Management Page

A switch panel for platform feature modules: each module can be enabled or disabled independently.

## Definition

Module status = enabled/disabled; entries for disabled modules are hidden, while their configurations are retained.

## How to Self-Check When Numbers Do Not Match

- 1. The menu remains after disabling: entry hiding has a caching delay, and the entry will disappear after refreshing.
- The module is enabled, but the feature is unavailable: the underlying service required by the module may not be ready. Check the dependency status in the module details.
