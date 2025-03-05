#!/bin/bash -e

# Store original IFS config, so we can restore it at various stages
ORIG_IFS=$IFS

if [[ -z "${KAFKA_START_MODE}" || ( "${KAFKA_START_MODE}" != "kraft" && "${KAFKA_START_MODE}" != "zookeeper" ) ]]; then
    echo "WARNING: missing config: KAFKA_START_MODE must be defined as 'zookeeper' or 'kraft'. Set to 'kraft' by default."
    KAFKA_START_MODE="kraft"
fi

if [[ "$KAFKA_START_MODE" == "zookeeper" ]]; then
    if [[ -z "$KAFKA_ZOOKEEPER_CONNECT" ]]; then
        echo "ERROR: missing mandatory config: KAFKA_ZOOKEEPER_CONNECT"
        exit 1
    fi
    CONF_FILE=$KAFKA_HOME/config/server.properties
    
    if [[ -z "$KAFKA_BROKER_ID" ]]; then
        export KAFKA_BROKER_ID=$(( ${POD_NAME##*-} + 1 ))
    fi;
fi

if [[ "$KAFKA_START_MODE" == "kraft" ]]; then
    CONF_FILE=$KAFKA_HOME/config/kraft/server.properties
    
    if [[ -z "$KAFKA_NODE_ID" ]]; then
        export KAFKA_NODE_ID=$(( ${POD_NAME##*-} + 1 ))
    fi;
fi

if [[ -z "$KAFKA_LISTENERS" ]]; then
    echo "ERROR: missing mandatory config: KAFKA_LISTENERS"
    exit 1
fi;

if [[ -z "$KAFKA_ADVERTISED_LISTENERS" ]]; then
    echo "ERROR: missing mandatory config: KAFKA_ADVERTISED_LISTENERS"
    exit 1
fi;

if [[ -z "$KAFKA_PORT" ]]; then
    export KAFKA_PORT=9092
fi;

create-topics.sh &
unset KAFKA_CREATE_TOPICS

#Issue newline to config file in case there is not one already
echo "" >> "$CONF_FILE"

(
    function updateConfig() {
        key=$1
        value=$2
        file=$3

        # Omit $value here, in case there is sensitive information
        echo "[Configuring] '$key' in '$file'"

        # If config exists in file, replace it. Otherwise, append to file.
        if grep -E -q "^#?$key=" "$file"; then
            if [[ "$key" == "controller.quorum.voters" ]]; then
                sed -i '/^controller\.quorum\.voters/d' "$file"
                echo "$key=$value" >> "$file"
            else
                sed -r -i "s@^#?$key=.*@$key=$value@g" "$file" #note that no config values may contain an '@' char
            fi
        else
            echo "$key=$value" >> "$file"
        fi
    }

    EXCLUSIONS="|KAFKA_VERSION|KAFKA_HOME|KAFKA_DEBUG|KAFKA_GC_LOG_OPTS|KAFKA_HEAP_OPTS|KAFKA_JMX_OPTS|KAFKA_JVM_PERFORMANCE_OPTS|KAFKA_LOG|KAFKA_OPTS|KAFKA_BOOTSTRAP_SERVER|KAFKA_START_MODE|"

    # Read in env as a new-line separated array. This handles the case of env variables have spaces and/or carriage returns. See #313
    IFS=$'\n'
    for VAR in $(env)
    do
        env_var=$(echo "$VAR" | cut -d= -f1)
        if [[ "$EXCLUSIONS" = *"|$env_var|"* ]]; then
            echo "Excluding $env_var from broker config"
            continue
        fi

        if [[ $env_var =~ ^KAFKA_ ]]; then
            kafka_name=$(echo "$env_var" | cut -d_ -f2- | tr '[:upper:]' '[:lower:]' | tr _ .)
            updateConfig "$kafka_name" "${!env_var}" "$CONF_FILE"
        fi

        if [[ $env_var =~ ^LOG4J_ ]]; then
            log4j_name=$(echo "$env_var" | tr '[:upper:]' '[:lower:]' | tr _ .)
            updateConfig "$log4j_name" "${!env_var}" "$KAFKA_HOME/config/log4j.properties"
        fi
    done
)

function formatKraft() {
    if [[ -z "$KAFKA_CLUSTER_ID" ]]; then
        echo "ERROR: missing mandatory config: KAFKA_CLUSTER_ID"
        exit 1
    fi;

    $KAFKA_HOME/bin/kafka-storage.sh format -t "$KAFKA_CLUSTER_ID" -c "$CONF_FILE"
}

if [[ "$KAFKA_START_MODE" == "kraft" ]]; then
    meta_properties_file="${KAFKA_METADATA_LOG_DIRS}/meta.properties"

    if [[ -f "$meta_properties_file" ]]; then
        echo "The meta.properties file exists, checking the cluster.id..."

        cluster_id=$(grep "^cluster.id=" "$meta_properties_file" | cut -d= -f2)
        if [[ -n "$cluster_id" ]]; then
            echo "Cluster ID found: $cluster_id"
        else
            echo "Cluster ID not found in $meta_properties_file. Initializing the storage."
            formatKraft
        fi
    else
        echo "The meta.properties file does not exist. Initializing the storage."
        formatKraft
    fi
    
fi

exec "$KAFKA_HOME/bin/kafka-server-start.sh" $CONF_FILE