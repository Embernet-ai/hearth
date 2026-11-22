# Hearth

A self-hosted all-in-one workspace. Work orders, assets, tasks, and documents sit
on one entity spine, with attachments verified by digest, permissions through
grants, live updates over SSE, local accounts, and generic OIDC single sign-on.

One Go binary, Postgres, and Garage for files. That is the whole system.

## Before installing

Create the credentials in the target namespace. The chart deliberately creates
none, because a chart that generates its own either rotates them on every apply,
breaking every session, or needs a guard that silently does nothing.

    kubectl -n hearth create secret generic hearth \
      --from-literal=POSTGRES_PASSWORD="$(openssl rand -hex 24)" \
      --from-literal=S3_ACCESS_KEY=hearth \
      --from-literal=S3_SECRET_KEY="$(openssl rand -hex 24)" \
      --from-literal=OIDC_CLIENT_SECRET="<from the app registration>"

The image, `ghcr.io/embernet-ai/hearth-community`, is public, so a cluster pulls it with no pull secret.

## TLS

Pick one of three. Leave `ingress.tls.certResolver` enabled where Traefik should order the certificate itself (TLS-ALPN-01 on
:443, for a cluster whose :80 does not reach it). Turn both it and `certManager` off where Traefik's default store already holds
a wildcard for the host: the host is still listed under `tls`, with no resolver and no secret, and gets that certificate (a cluster with a wildcard certificate does this). Enable `certManager` only where :80 can be answered.

Never enable both. Naming a `tls.secretName` makes Traefik serve that secret in
preference to its own resolver, so a secret nothing can issue means the host
serves nothing valid.

## First run

The first visit shows a setup page that creates the owner account. Local accounts
and single sign-on both work from then on.
