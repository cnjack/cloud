/**
 * Keep live output at the bottom, but open a restored long answer at its
 * beginning. Otherwise a completed response taller than the viewport looks as
 * though its first half never rendered.
 */
export function followConversationScroll(conversation: HTMLElement, status?: string): void {
  const assistantMessages = Array.from(
    conversation.querySelectorAll<HTMLElement>('[data-testid="thread-message-assistant"]'),
  );
  const latestMessage = assistantMessages.at(-1);

  if (status !== 'running' && latestMessage) {
    const conversationRect = conversation.getBoundingClientRect();
    const messageRect = latestMessage.getBoundingClientRect();
    if (messageRect.height > conversation.clientHeight) {
      const target = conversation.scrollTop + messageRect.top - conversationRect.top;
      conversation.scrollTop = Math.max(0, target);
      return;
    }
  }

  conversation.scrollTop = conversation.scrollHeight;
}
