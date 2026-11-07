set quiet
set dotenv-load

import 'just/dev.just'

mod go 'just/go.just'
mod docs 'just/docs.just'
mod ci 'just/ci.just'

default:
    @just --list
