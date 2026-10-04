# AGENTS.md

## Visão geral

`goci` é uma ferramenta em Go (go 1.19) que lança instâncias de computação na OCI de forma contínua. A aplicação inteira vive em um único arquivo, `main.go`, e roda como container (Kubernetes Job/Deployment ou docker compose). Observabilidade é feita via OpenTelemetry com exporter Prometheus na porta `2223`.

Módulo: `mol.net.br/goci` (dependências vendorizadas em `vendor/`).

## Comandos

```sh
# Compilar (usa vendor/ — não requer rede)
go build ./...

# Vetor estático
go vet ./...

# Testes
# Não há testes no repositório hoje.

# Binário estático (mesmo do Dockerfile)
CGO_ENABLED=0 go build -ldflags '-extldflags "-static" -w -s' -tags timetzdata

# Imagem Docker
docker build -t pedromol/goci .
```

Nenhum comando de lint/typecheck além de `go vet ./...` está configurado.

## Convenções de código

- Apenas `package main`; não há divisão em pacotes.
- Config 100% via variáveis de ambiente (ver tabela em `README.md`).
- Erros de runtime são tratados com `log.Fatal`; não há tratamento de erros granulares.
- Sem comentários de código; nomes de variáveis de ambiente são `UPPER_SNAKE`.

## Arquitetura e pontos de atenção

- `conf` (struct `config`) é uma variável global; o retry callback `shouldRetry` acessa `conf.counter`, `conf.messageRegex` e `conf.delay` diretamente.
- `shouldRetry` **sempre retorna `true`** — a operação nunca desiste. O controle de ritmo é feito pelo delay global `conf.delay`, que é lido/escrito nesse callback.
  - **HTTP 429** → `conf.delay++` (backoff aditivo, +1s por ocorrência).
  - **Outros erros** → decai `conf.delay--` apenas se já passaram 5 minutos desde o último decremento (`lastDelayInc`).
  - O delay nunca é zerado pelo código; o mínimo prático é garantido pelo ritmo de decaimento.
- Depois do callback de retry, o loop principal ainda dorme `conf.delay` segundos antes da próxima chamada `LaunchInstance`.
- O erro retornado por `c.LaunchInstance` é ignorado no loop principal — a visibilidade vem das métricas. O único uso do retorno é detectar sucesso para a notificação: `if err == nil { notifyInstanceLaunched(response.Instance) }`.
- `core.LaunchInstanceResponse` embute `Instance` como **valor** (não ponteiro), então `notifyInstanceLaunched` recebe `core.Instance`.
- O regex `Message: (.+)\.?` (`conf.messageRegex`) extrai a mensagem de erro do OCI para os atributos da métrica `oci_requests`.
- `PRIVATE_KEY` é passada com `\n` literal; o código converte via `strings.Replace(..., "\\n", "\n", -1)`.

## Notificações (Telegram)

- `telegramEnabled()` retorna `true` apenas se `TELEGRAM_TOKEN` **e** `TELEGRAM_CHAT_ID` estiverem preenchidos — a notificação é opcional e o goci funciona normalmente sem ela.
- `notifyInstanceLaunched(core.Instance)` monta a mensagem (nome, OCID, shape, AD, região, `TimeCreated` em RFC3339) e chama `notifyTelegram`.
- `notifyTelegram` faz `POST` form-encoded em `https://api.telegram.org/bot<token>/sendMessage` com timeout de 10s, sem nenhuma lib externa (só `net/http`).
- Falha de envio só gera `log.Printf` — nunca derruba o processo. O erro de rede é desembrulhado de `*url.Error` para **não vazar o token do bot nos logs**; respostas não-200 imprimem status + body.

## Observabilidade

- `go serveMetrics()` escuta `:2223/metrics` com `promhttp.Handler()`.
- Métricas:
  - `oci_requests` (counter) — atributos `code` (status HTTP) e `message` (erro parseado).
  - `oci_requests_delay` (gauge, assíncrona) — valor atual de `conf.delay`.

## Autenticação OCI

- `common.NewRawConfigurationProvider(tenancy, user, region, fingerprint, privateKey, nil)` — autenticação por API Key, sem arquivo de config.
- Variáveis usadas: `TENANCY`, `USER` (OCID do usuário), `REGION`, `FINGERPRINT`, `PRIVATE_KEY`.

## Detalhes do launch de instância

- Shape fixa no request: `ShapeConfig` com 4 OCPUs e 24 GB de RAM (`Ocpus: 4`, `MemoryInGBs: 24`).
- `AvailabilityConfig`: live migration preferida + `RecoveryAction` = `RestoreInstance`.
- `InstanceOptions`: `AreLegacyImdsEndpointsDisabled: false` (endpoints IMDS legados habilitados).
- VNIC: IP público habilitado, display name, hostname label e subnet definidos por env.
- `SourceDetails` via imagem (`InstanceSourceViaImageDetails`).

## Infraestrutura

- `Dockerfile`: build multi-stage (`golang:alpine` → `scratch`), binário estático, expõe `2223`.
- `deployment.yaml`: Deployment no namespace `goci`, espera `goci-env` ConfigMap + Secret, `nodeSelector` `oci.oracle.com/shape: amd`.
- `job.tf`: Job Kubernetes via `kubernetes_manifest` (saída de `terraform plan` com `+`), roda `alpine:3.22`, clona `https://github.com/pedromol/goci.git` e executa `go run .`, mesmas envs `goci-env`, `backoffLimit: 0`, `ttlSecondsAfterFinished: 86400`.
- `.github/workflows/build.yml`: CI em `master`; buildx multi-plataforma (amd64/arm64/arm/v7) + push para Docker Hub (`DOCKERHUB_USERNAME`, `DOCKERHUB_TOKEN`).
- `docker-compose.yml.example`: exemplo local com todas as variáveis de ambiente em branco.
