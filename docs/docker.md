# Docker запуск

Основной Compose-стек запускает два контейнера: `model` с официальным
`llama.cpp:server-vulkan` и `app` с Qwenchik. Контейнер приложения использует
тот же network namespace, что и модель, поэтому существующая политика
`LLM_BASE_URL` остаётся loopback-only. На хост публикуется только
`127.0.0.1:8080`.

## Один раз

Нужны Docker Engine/Compose, NVIDIA Container Toolkit и Vulkan-драйвер. GPU-
образы llama.cpp upstream публикуются, но сам upstream предупреждает, что
GPU-варианты не проходят полноценную CI-проверку. Для текущего ноутбука стек
по умолчанию ориентирован на `Vulkan1` и RTX 3050.

Положите GGUF в `./models` и назовите его `Qwen3.5-4B-Q4_K_M.gguf`, либо
задайте `MODEL_FILE` и `MODELS_DIR` через локальный `.env`. Секреты в Git не
добавляйте.

Перед первым запуском проверьте развёрнутую конфигурацию:

```sh
docker compose config -q
```

## Запуск и остановка

```sh
docker compose up -d --build
docker compose ps
docker compose logs -f model app
```

После статуса `healthy` откройте <http://127.0.0.1:8080>. Остановка:

```sh
docker compose down
```

Данные приложения живут в именованном volume `qwenchik-data`; `down` его не
удаляет. Для удаления данных нужна отдельная явная команда
`docker compose down --volumes`.

## Настройки и ограничения

Оставлены консервативные значения: один inference slot, context 32768,
q8 KV-cache, четыре CPU-потока, CPU/RAM/PID limits и температурный resource
guard. Изменяйте их только после контроля `sensors`; контейнеризация сама по
себе не устраняет перегрев.

Основные локальные переменные:

```dotenv
MODEL_FILE=Qwen3.5-4B-Q4_K_M.gguf
LLAMA_DEVICE=Vulkan1
LLAMA_GPU_LAYERS=99
LLAMA_THREADS=4
LLAMA_THREADS_BATCH=4
LLM_CONTEXT_LENGTH=32768
```

Для CPU-only режима используйте CPU-образ llama.cpp и отдельный override,
который удаляет `gpus`, `--device` и `-ngl`; такой режим обычно заметно медленнее
и может держать CPU горячее. Для CUDA используйте официальный
`ghcr.io/ggml-org/llama.cpp:server-cuda` вместе с NVIDIA Container Toolkit.

В контейнер приложения намеренно не пробрасывается `docker.sock`: это дало бы
процессу контроль над Docker-хостом и нарушило изоляцию. Поэтому sandbox/PDF
extraction будут показываться как недоступные, пока для них не будет спроектирован
отдельный rootless runtime.

## Диагностика

```sh
docker compose ps
curl --noproxy '*' --fail http://127.0.0.1:8080/api/health
docker compose logs --tail=100 model
```

Если `model` не становится healthy, сначала проверьте наличие GGUF, GPU runtime,
`LLAMA_DEVICE` и вывод `docker compose logs model`. Не запускайте повторные сборки
при срабатывании температурного alarm на CPU/RAM/GPU.
