import { describe, expect, it } from 'vitest';

import { followConversationScroll } from './conversationScroll';

function rect(top: number, height: number): DOMRect {
  return {
    x: 0,
    y: top,
    top,
    right: 0,
    bottom: top + height,
    left: 0,
    width: 0,
    height,
    toJSON: () => ({}),
  };
}

function conversationFixture({
  clientHeight = 500,
  scrollHeight = 1_400,
  scrollTop = 25,
  scrollLeft = 0,
  messageTop = 760,
  messageHeight = 620,
}: {
  clientHeight?: number;
  scrollHeight?: number;
  scrollTop?: number;
  scrollLeft?: number;
  messageTop?: number;
  messageHeight?: number;
} = {}) {
  const latest = {
    getBoundingClientRect: () => rect(messageTop, messageHeight),
  } as HTMLElement;
  const conversation = {
    clientHeight,
    scrollHeight,
    scrollTop,
    scrollLeft,
    getBoundingClientRect: () => rect(100, clientHeight),
    querySelectorAll: () => [latest],
  } as unknown as HTMLElement;
  return { conversation, latest };
}

describe('followConversationScroll', () => {
  it('resets horizontal drift while following content vertically', () => {
    const { conversation } = conversationFixture({ scrollLeft: 420 });

    followConversationScroll(conversation, 'running');

    expect(conversation.scrollLeft).toBe(0);
    expect(conversation.scrollTop).toBe(1_400);
  });

  it('anchors a restored long final answer at its beginning', () => {
    const { conversation } = conversationFixture();

    followConversationScroll(conversation, 'awaiting_input');

    expect(conversation.scrollTop).toBe(685);
  });

  it('keeps a running stream pinned to the bottom', () => {
    const { conversation } = conversationFixture();

    followConversationScroll(conversation, 'running');

    expect(conversation.scrollTop).toBe(1_400);
  });

  it('keeps a completed answer at the bottom when the whole message fits', () => {
    const { conversation } = conversationFixture({ messageHeight: 320 });

    followConversationScroll(conversation, 'succeeded');

    expect(conversation.scrollTop).toBe(1_400);
  });
});
