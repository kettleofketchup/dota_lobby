set quiet
set dotenv-load

import 'just/dev.just'

mod go 'just/go.just'
mod docs 'just/docs.just'
mod secrets 'just/secrets.just'
mod probe 'just/probe.just'
mod docker 'just/docker.just'
mod ci 'just/ci.just'
mod release 'just/release.just'

default:
    @just --list
