import openAIIcon from '../../../design/assets/provider-openai.svg';
import qwenIcon from '../../../design/assets/provider-qwen.svg';
import zhipuIcon from '../../../design/assets/provider-zhipu.svg';
import styles from './ProviderIcon.module.css';

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
