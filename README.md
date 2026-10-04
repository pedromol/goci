# goci

Ferramenta em Go que lança instâncias de computação na Oracle Cloud Infrastructure (OCI) de forma contínua, com backoff dinâmico contra rate limits e observabilidade via Prometheus/OpenTelemetry.

Pensado para rodar como um Job/Deployment em Kubernetes (ou container), **goci** cria uma instância OCI a partir de configuração via variáveis de ambiente, respeitando as cotas de API do OCI e expondo métricas para monitoramento.

## Como funciona

O programa é um único arquivo (`main.go`) que executa os seguintes passos:

1. **Inicializa observabilidade**: cria um `MeterProvider` OpenTelemetry com exporter Prometheus e sobe um servidor HTTP na porta `2223` (`/metrics`).
2. **Carrega configuração**: todas as opções vêm de variáveis de ambiente (veja a tabela abaixo).
3. **Autentica no OCI**: usa API Key (tenancy, user OCID, fingerprint, chave privada e região).
4. **Monta a requisição de launch**: uma instância com shape configurável, 4 OCPUs, 24 GB de RAM, IP público, hostname, chave SSH e recovery com `RestoreInstance` + live migration.
5. **Loop infinito**: chama `LaunchInstance` repetidamente, dormindo entre as tentativas de acordo com o backoff dinâmico.
6. **Notifica sucessos**: quando o launch retorna sem erro, envia uma mensagem no Telegram com os dados da instância criada.

### Backoff e retry

O `shouldRetry` registra uma callback customizada na retry policy do SDK do OCI:

- A cada tentativa, incrementa a métrica `oci_requests` com atributos de **status code** e **mensagem de erro** (extraída via regex `Message: (.+)\.?`).
- Em resposta **HTTP 429** (rate limit), o delay é **incrementado** em 1 segundo (backoff aditivo).
- Em qualquer outra resposta, o delay **decresce** em 1 segundo, mas apenas se já passaram **5 minutos** desde o último decremento — impedindo que o delay caia rápido demais.
- O programa dorme `delay` segundos antes de cada tentativa. O delay inicial é de **31 segundos**.

## Configuração (variáveis de ambiente)

| Variável | Descrição |
| --- | --- |
| `INSTANCE_SHAPE` | Shape da instância (ex.: `VM.Standard.E4.Flex`) |
| `INSTANCE_NAME` | Nome (display name) da instância |
| `INSTANCE_IMAGE` | OCID da imagem |
| `INSTANCE_SUBNET` | OCID da subnet |
| `INSTANCE_AD` | Availability domain |
| `INSTANCE_COMPARTMENT` | OCID do compartment |
| `INSTANCE_SSHAUTHORIZED` | Chave SSH pública autorizada |
| `VNIC_DISPLAY_NAME` | Nome da VNIC |
| `VNIC_HOSTNAME` | Hostname label da VNIC |
| `USER` | OCID do usuário da API |
| `FINGERPRINT` | Fingerprint da API key |
| `PRIVATE_KEY` | Chave privada PEM (escapamentos `\n` são convertidos para quebras de linha) |
| `TENANCY` | OCID da tenancy |
| `REGION` | Região OCI (ex.: `sa-saopaulo-1`) |
| `TELEGRAM_TOKEN` | Token do bot do Telegram (opcional). Sem ele a notificação é desabilitada |
| `TELEGRAM_CHAT_ID` | ID do chat/canal de destino da notificação (opcional) |

## Notificações (Telegram)

A cada `LaunchInstance` bem-sucedido o goci envia uma mensagem ao Telegram:

```
✅ Instância OCI criada

Nome: goci-01
OCID: ocid1.instance.oc1.sa-saopaulo-1...
Shape: VM.Standard.E4.Flex
AD: Uocm:sa-saopaulo-1-ad-1
Região: sa-saopaulo-1
Criada em: 2026-01-01T00:00:00Z
```

A notificação é **opcional**: se `TELEGRAM_TOKEN` ou `TELEGRAM_CHAT_ID` estiverem vazios, nada é enviado. Falhas de envio apenas são logadas (`telegram: ...`) e nunca interrompem o loop — erros de launch continuam visíveis pelas métricas.

## Métricas

Expõe `/metrics` no Prometheus na porta `2223`:

| Métrica | Tipo | Descrição |
| --- | --- | --- |
| `oci_requests` | counter | Total de requisições HTTP por tipo (atributos: `code`, `message`) |
| `oci_requests_delay` | gauge | Delay atual entre requisições (o backoff) |

## Execução

### Local (docker compose)

Copie `docker-compose.yml.example` para `docker-compose.yml`, preencha as variáveis e rode:

```sh
docker compose up
```

### Kubernetes

- `deployment.yaml` — Deployment no namespace `goci` (espera os recursos `goci-env` ConfigMap/Secret com as variáveis de ambiente).
- `job.tf` — manifest Terraform (`kubernetes_manifest`) de um **Job** Kubernetes que clona o repositório em um container `alpine` e executa `go run .` com as mesmas variáveis de ambiente, agendado em nós de shape `amd`.

### Imagem

A imagem `pedromol/goci:latest` é publicada no Docker Hub pelo workflow de CI.

## Build

```sh
# Binário estático
CGO_ENABLED=0 go build -ldflags '-extldflags "-static" -w -s' -tags timetzdata

# Imagem Docker (multi-stage: golang:alpine -> scratch)
docker build -t pedromol/goci .
```

O `Dockerfile` produz uma imagem mínima `scratch` com CA certs, expondo a porta `2223`.

O workflow `.github/workflows/build.yml` faz build multi-plataforma (`linux/amd64`, `linux/arm64`, `linux/arm/v7`) com Buildx e envia para o Docker Hub a cada push na branch `master`.

## Estrutura

```
main.go                      # aplicação completa (build, loop e retry)
Dockerfile                   # build multi-stage para imagem scratch
deployment.yaml              # Deployment Kubernetes
job.tf                       # Job Kubernetes via Terraform (kubernetes_manifest)
docker-compose.yml.example   # exemplo de execução local
.github/workflows/build.yml  # CI: buildx multi-plataforma + push Docker Hub
```
