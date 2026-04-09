import type { IJsonModel } from "flexlayout-react";

export const defaultLayout: IJsonModel = {
  global: {
    tabEnablePopout: false,
    tabSetEnableMaximize: true,
    splitterSize: 5,
    tabEnableClose: true,
    tabEnableRename: true,
  },
  layout: {
    type: "row",
    weight: 100,
    children: [
      {
        type: "tabset",
        id: "main",
        weight: 100,
        children: [],
      },
    ],
  },
};
