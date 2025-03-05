#!/bin/bash

if [[ -z "$KAFKA_CREATE_TOPICS" ]]; then
    exit 0
fi

echo "create-topics.sh KAFKA_CREATE_TOPICS: $KAFKA_CREATE_TOPICS"

if [[ -z "$START_TIMEOUT" ]]; then
    START_TIMEOUT=600
fi

if [[ -z "$KAFKA_BOOTSTRAP_SERVER" ]]; then
    KAFKA_BOOTSTRAP_SERVER="localhost:9092"
fi

start_timeout_exceeded=false
count=0
step=10
while netstat -lnt | awk '$4 ~ /:'"$KAFKA_PORT"'$/ {exit 1}'; do
    echo "waiting for kafka to be ready"
    sleep $step;
    count=$((count + step))
    if [ $count -gt $START_TIMEOUT ]; then
        start_timeout_exceeded=true
        break
    fi
done

if $start_timeout_exceeded; then
    echo "Not able to auto-create topic (waited for $START_TIMEOUT sec)"
    exit 1
fi

# Expected format: name:partitions:replicas:extraconfigs (extraconfigs separated by semicolons)
IFS="${KAFKA_CREATE_TOPICS_SEPARATOR-,}"; for topicToCreate in $KAFKA_CREATE_TOPICS; do
    echo "creating topics: $topicToCreate"
    IFS=':' read -r -a topicConfig <<< "$topicToCreate"

    extraConfigs="${topicConfig[3]}"
    config=""
    if [ -n "$extraConfigs" ]; then
        # Dividir las configuraciones adicionales por comas y agregarlas al comando
        IFS=';' read -r -a configsArray <<< "$extraConfigs"
        for cfg in "${configsArray[@]}"; do
            config="$config --config=$cfg"
        done
    fi

    COMMAND="JMX_PORT='' ${KAFKA_HOME}/bin/kafka-topics.sh \\
		--create \\
		--bootstrap-server ${KAFKA_BOOTSTRAP_SERVER} \\
		--topic ${topicConfig[0]} \\
		--partitions ${topicConfig[1]} \\
		--replication-factor ${topicConfig[2]} \\
		${config} \\
		--if-not-exists &"
    eval "${COMMAND}"
done

wait
