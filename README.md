# vic-cloudless

Vector processing text all by himself.

# Get deps

**Install Go, gcc, g++, make, automake, git, and wget.**

# Build

(this can only build on Linux)

```
make
```

# Deploy

```
./deploy.sh <ip>
```

# How?

- This currently works via [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx). I chose to convert a Citrinet model to ONNX myself. It's the lowest quality one, but it still works pretty well (better than Vosk in my experience).
- I compiled onnxruntime for Vector's softfp OS with an XNNPACK backend, then compiled sherpa-onnx with that onnxruntime library.
- This was originally using a cut down Vosk model with a limited dictionary. **This is now full-fledged speech-to-text running on Vector.**

# Can I put in custom intents?

- Not yet, unless you know how to write Go.

