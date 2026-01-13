#!/bin/bash

cd /sherpa

./sherpa-onnx-offline-websocket-server  \
   --tokens=./citrinet-256-ls/tokens.txt \
   --nemo-ctc-model=./citrinet-256-ls/model.onnx \
   --num-threads=3 \
   --num-work-threads=4 \
   --log-file=/tmp/sherplog.txt \
   --decoding-method=greedy_search \
   --debug=false
