#!/bin/bash
#
# Создание A-record через svcapi (deck-api). 

# --- Настройки deck-api ---
BASE_URL="https://deck-api.ngcloud.ru/api/v1/index.cfm"
# TOKEN="<generated_technical_token>"
TOKEN=""
# --- Константы/START ---

# id сервиса "DNS Records"
SERVICE_ID="${SERVICE_ID:-111}"

# Id операции create сервиса "DNS Records"
SVC_OPERATION_ID="${SVC_OPERATION_ID:-45}"
#######
# Может пригодится
# delete = 46
# modify = 90
#######

# --- CFS: label -> id -> param. ---
# Эта переменная отображается в STEP-1
declare -A CFS_PARAM_IDS
CFS_LABELS_ORDER=()

# --- Константы/END ---

# --- Ввод аргументов/START ---

# --- Параметры операции create (label -> value). Позволяет настроить внутри скрипта ---
declare -A PARAMS=(
    ["UUID Зоны"]=""
    ["Тип DNS-записи"]=""
    ["Имя записи"]=""
    ["Значение записи"]=""
    ["TTL записи (в секундах)"]=""
)

# Тоже самое, что выше - но если хочется запускать скрипт с передачей аргументов
for arg in "$@"; do
    case "$arg" in
        ZONE_UID=*)    ZONE_UID="${arg#ZONE_UID=}" ;;
        RECORD_NAME=*) RECORD_NAME="${arg#RECORD_NAME=}" ;;
        RECORD_IP=*)   RECORD_IP="${arg#RECORD_IP=}" ;;
        RECORD_TTL=*)  RECORD_TTL="${arg#RECORD_TTL=}" ;;
        RECORD_TYPE=*) RECORD_TYPE="${arg#RECORD_TYPE=}" ;;
        TOKEN=*)       TOKEN="${arg#TOKEN=}" ;;
    esac
done

# Заполняем PARAMS с всех параметров
PARAMS["UUID Зоны"]="${ZONE_UID:-${PARAMS["UUID Зоны"]}}"
PARAMS["Тип DNS-записи"]="${RECORD_TYPE:-A}"
PARAMS["Имя записи"]="${RECORD_NAME:-${PARAMS["Имя записи"]}}"
PARAMS["Значение записи"]="${RECORD_IP:-${PARAMS["Значение записи"]}}"
PARAMS["TTL записи (в секундах)"]="${RECORD_TTL:-3600}"

# Я бы перенёс выше, но нужен токен для запросов
COMMON_HEADERS=(
    -H "accept: application/json"
    -H "Authorization: Bearer ${TOKEN}"
    -H "Content-Type: application/json"
)
# --- Ввод аргументов/END ---

# --- Логирование/START ---
# Цвета для вывода
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_step() { echo -e "${YELLOW}[STEP]${NC} $1"; }
log_debug() { echo -e "${BLUE}[DEBUG]${NC} $1"; }

# --- Логирование/END ---

# --- Проверка авторизации/START ---
# Проверяем ответ на ошибки авторизации

check_auth_error() {
    local response="$1"
    local step_name="$2"
    if echo "$response" | grep -q "401 Unauthorized" || echo "$response" | grep -q "token signature is invalid"; then
        log_error "Ошибка авторизации на шаге: ${step_name}"
        log_error "Токен недействителен. Обновите технический токен."
        return 1
    fi
    if echo "$response" | grep -q "^HTTP/[0-9.]* 401"; then
        log_error "HTTP 401 Unauthorized на шаге: ${step_name}"
        return 1
    fi
    return 0
}

# --- Проверка авторизации/END ---

# --- Болванка/START ---

usage() {
    echo "Usage:"
    echo "  env:  ZONE_UID=<uuid> RECORD_NAME=<name> RECORD_IP=<ip> TOKEN=<token> ./dns_record_create.sh"
    echo "  или: . dns_record_create.sh ZONE_UID=<uuid> RECORD_NAME=<name> RECORD_IP=<ip> TOKEN=<token>"
    echo
    echo "Обязательные параметры:"
    echo "  ZONE_UID     - UUID инстанса DNS-зоны (метка 'UUID Зоны')"
    echo "  RECORD_NAME  - Имя DNS-записи"
    echo "  RECORD_IP    - IP-адрес записи"
    echo "  TOKEN        - Bearer-токен deck-api"
    return 1
}
# --- Болванка/END ---

#####################################
# Отсюда начинает выполняться скрипт
#####################################
# --- Проверка обязательных параметров ---
if [ -z "$TOKEN" ]; then
    log_error "TOKEN обязателен"
    usage
    return 1
fi
for label in "UUID Зоны" "Имя записи" "Значение записи"; do
    val="${PARAMS[$label]:-}"
    if [ -z "$val" ]; then
        log_error "Параметр '${label}' не задан. Задайте ZONE_UID, RECORD_NAME, RECORD_IP или заполните PARAMS в скрипте."
        usage
        return 1
    fi
done

# --- Получение CFS-параметров операции ---
log_step "Получение CFS-параметров операции create..."
# Нужно так как не знаем ID CFS-Параметра. Получив его, можно через label сопоставить значение, чтобы потом его можно было передать в операцию
CFS_RESP=$(curl -s -S "${BASE_URL}/instanceOperations/default/${SVC_OPERATION_ID}?fields=operation,svcOperationId,cfsParams" \
    "${COMMON_HEADERS[@]}" 2>&1) || true

if ! check_auth_error "$CFS_RESP" "получение CFS операции"; then
    # debug
    # log_debug "Полный ответ сервера при получении CFS:"
    # echo "$CFS_RESP" | head -n 20
    log_info "Проверьте, что TOKEN получен через интерфейс Личного кабинета и не истёк."
    return 1
fi

# Сохраняем параметры в массив
while IFS=$'\t' read -r label id; do
    [ -z "$label" ] && continue
    CFS_PARAM_IDS["$label"]=$id
    CFS_LABELS_ORDER+=("$label")
done < <(echo "$CFS_RESP" | jq -r '.svcOperation.cfsParams[] | "\(.label)\t\(.svcOperationCfsParamId)"')
log_info "Получено CFS-параметров: ${#CFS_LABELS_ORDER[@]} (label -> id)"

# --- Вывод CFS-параметров ---
# Нужно для отладки в основном 
#
# log_debug "CFS -> ID -> PARAM"
# 
# {
#     printf '%s\t%s\t%s\n' "LABEL" "ID" "PARAM"
#     for label in "${CFS_LABELS_ORDER[@]}"; do
#         cfs_id="${CFS_PARAM_IDS[$label]:-}"
#         val="${PARAMS[$label]:-}"
#         printf '%s\t%s\t%s\n' "$label" "$cfs_id" "$val"
#     done
# } | column -t -s $'\t'

# --- Создание инстанса ---
log_step "Создание инстанса..."
# Важный момент. На этапе создания инстанса мы не знаем его UUID, поэтому для идентификации используем displayName
# Далее по этому displayName мы будем получать UUID

DISPLAY_NAME="dnsrecord-${PARAMS["Имя записи"]:-record}"
INSTANCE_BODY=$(jq -n \
    --argjson sid "$SERVICE_ID" \
    --arg name "$DISPLAY_NAME" \
    '{serviceId: $sid, displayName: $name, descr: ""}')

CREATE_INSTANCE_RESP=$(curl -s -i -S -X POST "${BASE_URL}/instances" "${COMMON_HEADERS[@]}" -d "$INSTANCE_BODY" 2>&1) || true
if ! check_auth_error "$CREATE_INSTANCE_RESP" "создание инстанса"; then
    log_debug "Ответ сервера:"
    echo "$CREATE_INSTANCE_RESP" | head -n 30
    return 1
fi
# Проверяем успешность создания инстанса
HTTP_CODE=$(echo "$CREATE_INSTANCE_RESP" | grep -o "HTTP/[0-9.]* [0-9]*" | awk '{print $2}')
if [[ "$HTTP_CODE" != "2"* ]]; then
    log_error "Ошибка при создании инстанса. HTTP: ${HTTP_CODE}"
    echo "$CREATE_INSTANCE_RESP" | tail -n 20
    return 1
fi

# Получаем UUID инстанса по displayName
# Получаем вначале список по serviceId
INSTANCES_GET=$(curl -s -S "${BASE_URL}/instances?fields=instanceUid,displayName,instanceConfigDtCreated&page=1&pageSize=100&serviceId=${SERVICE_ID}" \
    "${COMMON_HEADERS[@]}" 2>&1) || true
if ! check_auth_error "$INSTANCES_GET" "получение списка экземпляров"; then
    return 1
fi
# Далее отфильтровываем по displayName
INSTANCE_UID=$(echo "$INSTANCES_GET" | jq -r --arg name "$DISPLAY_NAME" '.results[] | select(.displayName==$name) | .instanceUid' | head -1)
if [ -z "$INSTANCE_UID" ] || [ "$INSTANCE_UID" = "null" ]; then
    log_error "Не удалось найти экземпляр с displayName=${DISPLAY_NAME} в GET instances"
    echo "$INSTANCES_GET" | jq '.' 2>/dev/null || echo "$INSTANCES_GET"
    return 1
fi
log_info "Экземпляр создан: ${INSTANCE_UID}"

# --- Создание операции ---
log_step "Создание операции create..."

# В случае если нужны будут операции modify / delete - можно избегать большинство шагов выше и начинать отсюда
# Процесс для остальных операций будет такой же, только CFS выше надо будет поправить или убрать (в delete к примеру не нужно передавать CFS)

OP_BODY=$(jq -n --arg iu "$INSTANCE_UID" '{instanceUid: $iu, operation: "create"}')
CREATE_OP_RESP=$(curl -s -i -S -X POST "${BASE_URL}/instanceOperations" "${COMMON_HEADERS[@]}" -d "$OP_BODY" 2>&1) || true

if ! check_auth_error "$CREATE_OP_RESP" "создание операции"; then
    log_debug "Ответ сервера:"
    echo "$CREATE_OP_RESP" | head -n 30
    return 1
fi

# ID операции берётся по хитрому
LOCATION=$(echo "$CREATE_OP_RESP" | grep -i location | head -1 | awk '{print $2}' | tr -d '\r\n\t ')
if [ -z "$LOCATION" ]; then
    log_error "Не удалось получить UUID операции"
    return 1
fi

OPERATION_UID=$(echo "$LOCATION" | sed 's|^\./||' | grep -oE '[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}' | head -1)
if [ -z "$OPERATION_UID" ]; then
    log_error "Не удалось извлечь UUID операции"
    return 1
fi
log_info "Операция создана: ${OPERATION_UID}"

# --- Добавление CFS-параметров ---
log_step "Добавление CFS-параметров..."

# Каждый CFS-параметр отдельно валидируется, поэтому на каждое добавление параметра - его надо пушить отдельно

# Это чтоб не писать кучу кода для каждого CFS-параметра
push_cfs() {
    local cfs_id=$1
    local value=$2
    local body
    body=$(jq -n --arg v "$value" --arg ou "$OPERATION_UID" --argjson cid "$cfs_id" \
        '{paramValue: $v, instanceOperationUid: $ou, svcOperationCfsParamId: $cid}')
    local code
    code=$(curl -s -o /dev/null -w "%{http_code}" -X POST "${BASE_URL}/instanceOperationCfsParams" "${COMMON_HEADERS[@]}" -d "$body")
    [ "$code" = "201" ] || [ "$code" = "200" ]
}

# Далее проходим по всем CFS-параметрам из созданной мапы и пушим их
for label in "${CFS_LABELS_ORDER[@]}"; do
    cfs_id="${CFS_PARAM_IDS[$label]:-}"
    [ -z "$cfs_id" ] && continue
    val="${PARAMS[$label]:-}"
    if [ -z "$val" ]; then
        log_debug "Пропуск CFS '${label}' (значение не задано)"
        continue
    fi
    if ! push_cfs "$cfs_id" "$val"; then
        log_error "Ошибка отправки CFS '${label}' (id=${cfs_id})"
        return 1
    fi
    log_info "  '${label}' = ${val}"
done
log_info "CFS-параметры отправлены."

# --- Запуск операции ---
log_step "Запуск операции..."

# Запускаем операцию
RUN_RESPONSE=$(curl -s -i -X POST "${BASE_URL}/instanceOperations/${OPERATION_UID}/run" \
    "${COMMON_HEADERS[@]}")

if ! check_auth_error "$RUN_RESPONSE" "запуск операции"; then
    return 1
fi
HTTP_CODE=$(echo "$RUN_RESPONSE" | grep HTTP | awk '{print $2}')
if [[ "$HTTP_CODE" != "2"* ]]; then
    log_error "Ошибка при запуске операции. HTTP код: ${HTTP_CODE}"
    return 1
fi
log_info "Операция запущена"

# --- Ожидание завершения операции ---
log_step "Ожидание завершения операции..."

MAX_ATTEMPTS=60
ATTEMPT=0
SLEEP_TIME=5
IS_SUCCESSFUL=""
COMPLETED_AT=""
ERROR_LOG=""

while [ $ATTEMPT -lt $MAX_ATTEMPTS ]; do
    ATTEMPT=$((ATTEMPT + 1))
    STATUS_RESPONSE=$(curl -s "${BASE_URL}/instanceOperations/${OPERATION_UID}" \
        "${COMMON_HEADERS[@]}" 2>&1)

    if ! check_auth_error "$STATUS_RESPONSE" "проверка статуса"; then
        return 1
    fi

    if echo "$STATUS_RESPONSE" | jq -e '.instanceOperation' >/dev/null 2>&1; then
        IS_SUCCESSFUL=$(echo "$STATUS_RESPONSE" | jq -r '.instanceOperation.isSuccessful // false')
        COMPLETED_AT=$(echo "$STATUS_RESPONSE" | jq -r '.instanceOperation.dtFinish // empty')
        ERROR_LOG=$(echo "$STATUS_RESPONSE" | jq -r '.instanceOperation.errorLog // empty')
    fi

    if [ "$IS_SUCCESSFUL" = "true" ] && [ -n "$COMPLETED_AT" ]; then
        log_info "Операция успешно завершена в ${COMPLETED_AT}"
        break
    elif [ "$IS_SUCCESSFUL" = "false" ] && [ -n "$COMPLETED_AT" ]; then
        log_error "Операция завершилась с ошибкой"
        if [ -n "$ERROR_LOG" ] && [ "$ERROR_LOG" != "null" ]; then
            log_error "Ошибка: ${ERROR_LOG}"
        fi
        echo "$STATUS_RESPONSE" | jq '.instanceOperation' 2>/dev/null || echo "$STATUS_RESPONSE"
        return 1
    elif [ -n "$COMPLETED_AT" ] && [ "$IS_SUCCESSFUL" = "false" ]; then
        log_error "Операция завершилась (${COMPLETED_AT}), но статус успеха не определен"
        if [ -n "$ERROR_LOG" ] && [ "$ERROR_LOG" != "null" ]; then
            log_error "Ошибка: ${ERROR_LOG}"
        fi
        echo "$STATUS_RESPONSE" | jq '.instanceOperation' 2>/dev/null || echo "$STATUS_RESPONSE"
        return 1
    fi

    log_info "Операция еще выполняется... попытка ${ATTEMPT}/${MAX_ATTEMPTS}"
    sleep $SLEEP_TIME
done

if [ $ATTEMPT -eq $MAX_ATTEMPTS ]; then
    log_error "Превышено время ожидания завершения операции"
    return 1
fi

# --- Итог ---
log_step "Готово."
log_info "Имя инстанса: ${DISPLAY_NAME}"
log_info "Id инстанса: ${INSTANCE_UID}"