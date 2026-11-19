type ServerModule = {
  agent?: {
    abilities?: readonly {
      mcp: {
        tool: string;
      };
    }[];
  };
};

export const SERVER_MODULES: readonly ServerModule[] = [];
