interface StrikeReadableStdin extends AsyncIterable<Uint8Array | string> {
  setEncoding(encoding: "utf8"): void;
}

interface StrikeWritableStream {
  write(chunk: string): boolean;
  once(event: "drain", listener: () => void): void;
}

declare const process: {
  stdin: StrikeReadableStdin;
  stdout: StrikeWritableStream;
  stderr: StrikeWritableStream;
  exitCode?: number;
};
