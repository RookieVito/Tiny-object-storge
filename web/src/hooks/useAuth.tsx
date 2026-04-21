import { createContext, useContext, useState, useCallback, useMemo, type ReactNode } from 'react';
import type { S3Config } from '../api/client';

interface AuthContextValue {
  config: S3Config | null;
  login: (config: S3Config) => void;
  logout: () => void;
}

const AuthContext = createContext<AuthContextValue>({
  config: null,
  login: () => {},
  logout: () => {},
});

export function AuthProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<S3Config | null>(() => {
    const saved = sessionStorage.getItem('tos_config');
    if (saved) {
      try {
        return JSON.parse(saved);
      } catch {
        return null;
      }
    }
    return null;
  });

  const login = useCallback((cfg: S3Config) => {
    sessionStorage.setItem('tos_config', JSON.stringify(cfg));
    setConfig(cfg);
  }, []);

  const logout = useCallback(() => {
    sessionStorage.removeItem('tos_config');
    setConfig(null);
  }, []);

  const value = useMemo(() => ({ config, login, logout }), [config, login, logout]);

  return (
    <AuthContext.Provider value={value}>
      {children}
    </AuthContext.Provider>
  );
}

export function useAuth(): AuthContextValue {
  return useContext(AuthContext);
}
