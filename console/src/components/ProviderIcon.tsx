import styles from './ProviderIcon.module.css';

const assetUrl = (name: string) => `${import.meta.env.BASE_URL}${name}`;
const openAIIcon = assetUrl('provider-openai.svg');
const qwenIcon = assetUrl('provider-qwen.svg');
const zhipuIcon = assetUrl('provider-zhipu.svg');

function iconFor(kind: string, name: string): { key: 'openai' | 'qwen' | 'zhipu'; src: string } {
  const identity = `${kind} ${name}`.toLowerCase();
  if (/qwen|dashscope|alibaba/.test(identity)) return { key: 'qwen', src: qwenIcon };
  if (/zhipu|bigmodel|glm/.test(identity)) return { key: 'zhipu', src: zhipuIcon };
  return { key: 'openai', src: openAIIcon };
}

export function ProviderIcon({ kind, name }: { kind: string; name: string }) {
  const icon = iconFor(kind, name);
  return <span className={styles.wrap} data-provider-icon={icon.key} aria-hidden="true"><img src={icon.src} alt="" /></span>;
}
