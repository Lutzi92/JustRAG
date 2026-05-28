import { createContext, useContext } from 'react';
import { useIsMobile } from '../hooks/useIsMobile';

const MobileContext = createContext(false);

export function MobileProvider({ children }: { children: React.ReactNode }) {
    const isMobile = useIsMobile();
    return <MobileContext.Provider value={isMobile}>{children}</MobileContext.Provider>;
}

export function useIsMobileContext(): boolean {
    return useContext(MobileContext);
}
