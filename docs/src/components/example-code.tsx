import httpLab from '../../examples/http-lab.yaml?raw';
import sshLlm from '../../examples/ssh-llm.yaml?raw';
import mcpTripwire from '../../examples/mcp-tripwire.yaml?raw';
import tcpRedis from '../../examples/tcp-redis.yaml?raw';
import coreObservability from '../../examples/core-observability.yaml?raw';

const examples = {
  'http-lab.yaml': httpLab,
  'ssh-llm.yaml': sshLlm,
  'mcp-tripwire.yaml': mcpTripwire,
  'tcp-redis.yaml': tcpRedis,
  'core-observability.yaml': coreObservability,
} as const;

export type ExampleName = keyof typeof examples;

export function ExampleCode({ name }: { name: ExampleName }) {
  return (
    <figure className="not-prose my-6 overflow-hidden rounded-xl border border-fd-border bg-fd-card">
      <figcaption className="flex items-center justify-between border-b border-fd-border px-4 py-2 text-sm">
        <code>{name}</code>
        <a className="font-medium text-fd-primary hover:underline" href={`/examples/${name}`} download>
          Download
        </a>
      </figcaption>
      <pre className="overflow-x-auto p-4 text-sm leading-6"><code>{examples[name]}</code></pre>
    </figure>
  );
}
