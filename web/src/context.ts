import { createContext, useContext } from 'react';
import type { Message } from './i18n';
import type { Session } from './types';
export const AppContext = createContext<{
  session: Session;
  notify: (message: Message) => void;
} | null>(null);
export function useApp() {
  const context = useContext(AppContext);
  if (!context) throw new Error('Missing application context');
  return context;
}
