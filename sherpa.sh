#!/bin/bash

cd /sherpa

./sherpa-onnx-offline-websocket-server  \
   --moonshine-preprocessor=./sherpa-onnx-moonshine-tiny-en-int8/preprocess.onnx \
   --moonshine-encoder=./sherpa-onnx-moonshine-tiny-en-int8/encode.int8.onnx \
   --moonshine-uncached-decoder=./sherpa-onnx-moonshine-tiny-en-int8/uncached_decode.int8.onnx \
   --moonshine-cached-decoder=./sherpa-onnx-moonshine-tiny-en-int8/cached_decode.int8.onnx \
   --tokens=./sherpa-onnx-moonshine-tiny-en-int8/tokens.txt \
   --num-threads=3 \
   --num-work-threads=4 \
   --log-file=/tmp/sherplog.txt
